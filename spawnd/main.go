package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/immesys/spawnpoint/objects"

	docker "github.com/fsouza/go-dockerclient"
	bw2 "gopkg.in/immesys/bw2bind.v5"
	yaml "gopkg.in/yaml.v2"
)

var bwClient *bw2.BW2Client
var PrimaryAccessChain string
var Cfg *DaemonConfig
var olog chan SLM

var (
	totalMem           uint64 // Memory dedicated to Spawnpoint, in MiB
	totalCpuShares     uint64 // CPU shares for Spawnpoint, 1024 per core
	availableMem       uint64
	availableCpuShares uint64
	availLock          sync.Mutex
)

var (
	runningServices map[string]*Manifest
	runningSvcsLock sync.Mutex
)

var eventCh chan *docker.APIEvents

const HEARTBEAT_PERIOD = 5

type SLM struct {
	Service string
	Message string
}

func readConfigFromFile(fileName string) (*DaemonConfig, error) {
	configContents, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	config := &DaemonConfig{}
	err = yaml.Unmarshal(configContents, config)
	if err != nil {
		return nil, err
	} else {
		return config, nil
	}
}

func initializeBosswave() (*bw2.BW2Client, error) {
	if Cfg.LocalRouter == "" {
		Cfg.LocalRouter = "127.0.0.1:28589"
	}

	client, err := bw2.Connect(Cfg.LocalRouter)
	if err != nil {
		return nil, err
	}

	_, err = client.SetEntityFile(Cfg.Entity)
	if err != nil {
		return nil, err
	}

	client.OverrideAutoChainTo(true)
	return client, nil
}

func main() {
	runningServices = make(map[string]*Manifest)
	runningSvcsLock = sync.Mutex{}

	var err error
	Cfg, err = readConfigFromFile("config.yml")
	if err != nil {
		fmt.Println("Config file error:", err)
		os.Exit(1)
	}

	availableCpuShares = Cfg.CpuShares
	rawMem := Cfg.MemAlloc
	memAlloc, err := parseMemAlloc(rawMem)
	if err != nil {
		fmt.Println("Invalid Spawnpoint memory allocation:", err)
		os.Exit(1)
	}
	availableMem = memAlloc

	bwClient, err = initializeBosswave()
	if err != nil {
		fmt.Println("Failed to connect to Bosswave router and establish permissions:", err)
		os.Exit(1)
	} else {
		fmt.Println("Successfully connected to router")
	}

	// Start docker connection
	eventCh, err = ConnectDocker()
	if err != nil {
		fmt.Println("Could not connect to Docker:", err)
		os.Exit(1)
	}
	go monitorDockerEvents(&eventCh)

	newconfigs, err := bwClient.Subscribe(&bw2.SubscribeParams{URI: path("ctl/cfg")})
	if err != nil {
		fmt.Println("Could not subscribe to config URI: ", err)
		os.Exit(1)
	}

	restart, err := bwClient.Subscribe(&bw2.SubscribeParams{URI: path("ctl/restart")})
	if err != nil {
		fmt.Println("Could not subscribe to restart URI: ", err)
		os.Exit(1)
	}

	stop, err := bwClient.Subscribe(&bw2.SubscribeParams{URI: path("ctl/stop")})
	if err != nil {
		fmt.Println("Could not subscribe to stop URI: ", err)
		os.Exit(1)
	}

	olog = make(chan SLM, 100)
	go doOlog()
	go heartbeat()
	fmt.Println("spawnpoint active")

	for {
		select {
		case ncfg := <-newconfigs:
			handleConfig(ncfg)

		case r := <-restart:
			svcName := ""
			for _, po := range r.POs {
				if po.IsTypeDF(bw2.PODFString) {
					svcName = string(po.GetContents())
				}
			}

			if svcName != "" {
				restartService(svcName, false)
			} else {
				respawn()
			}

		case s := <-stop:
			svcName := ""
			for _, po := range s.POs {
				if po.IsTypeDF(bw2.PODFString) {
					svcName = string(po.GetContents())
				}
			}

			if svcName != "" {
				stopService(svcName)
			}
		}
	}
}

func PubLog(svcname string, POs []bw2.PayloadObject) {
	err := bwClient.Publish(&bw2.PublishParams{
		URI:            path("info/spawn/" + svcname + "/log"),
		PayloadObjects: POs,
	})
	if err != nil {
		fmt.Println("Error publishing log: ", err)
	}
}

func doOlog() {
	for {
		POs := make(map[string][]bw2.PayloadObject)
		msg1 := <-olog
		po1, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog,
			objects.SPLog{
				time.Now().UnixNano(),
				Cfg.Alias,
				msg1.Service,
				msg1.Message,
			})
		if err != nil {
			panic(err)
		}
		POs[msg1.Service] = []bw2.PayloadObject{po1}

		//Opportunistically add another few messages if they are in the channel
		for i := 0; i < len(olog); i++ {
			msgN := <-olog
			poN, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog,
				objects.SPLog{
					time.Now().UnixNano(),
					Cfg.Alias,
					msgN.Service,
					msgN.Message,
				})
			if err != nil {
				panic(err)
			}

			exslice, ok := POs[msgN.Service]
			if ok {
				POs[msgN.Service] = append(exslice, poN)
			} else {
				POs[msgN.Service] = []bw2.PayloadObject{poN}
			}
		}

		for svc, poslice := range POs {
			PubLog(svc, poslice)
		}
	}
}

func heartbeat() {
	for {
		availLock.Lock()
		mem := availableMem
		shares := availableCpuShares
		availLock.Unlock()

		fmt.Fprintf(os.Stderr, "shares: %v, mem: %v\n", shares, mem)
		msg := objects.SpawnPointHb{
			Cfg.Alias,
			time.Now().Format(time.RFC3339),
			mem,
			shares,
		}
		po, err := bw2.CreateYAMLPayloadObject(bw2.FromDotForm(bw2.PODFSpawnpointHeartbeat), msg)
		if err != nil {
			panic(err)
		}

		hburi := path("info/spawn/!heartbeat")
		err = bwClient.Publish(&bw2.PublishParams{
			URI:                hburi,
			PrimaryAccessChain: PrimaryAccessChain,
			ElaboratePAC:       bw2.ElaborateFull,
			Persist:            true,
			PayloadObjects:     []bw2.PayloadObject{po},
		})
		if err != nil {
			panic(err)
		}
		time.Sleep(HEARTBEAT_PERIOD * time.Second)
	}
}

func path(suffix string) string {
	return Cfg.Path + "/" + suffix
}

func respawn() {
	olog <- SLM{"meta", "doing respawn"}
	runningSvcsLock.Lock()
	for _, manifest := range runningServices {
		cnt, err := RestartContainer(manifest, true)
		if err != nil {
			fmt.Println("ERROR IN CONTAINER: ", err)
		} else {
			fmt.Println("Container started ok")
			manifest.Container = cnt
		}
	}
	runningSvcsLock.Unlock()
}

func restartService(serviceName string, rebuildImage bool) {
	olog <- SLM{serviceName, "attempting restart"}
	runningSvcsLock.Lock()
	for name, manifest := range runningServices {
		if name == serviceName {
			cnt, err := RestartContainer(manifest, rebuildImage)
			if err != nil {
                olog <- SLM{serviceName, "restart failed"}
				fmt.Println("ERROR IN CONTAINER: ", err)
			} else {
                olog <- SLM{serviceName, "restart successful!"}
				fmt.Println("Container started ok")
				manifest.Container = cnt
			}
		}
	}
	runningSvcsLock.Unlock()
}

func stopService(serviceName string) {
	olog <- SLM{serviceName, "attempting to stop container"}
	runningSvcsLock.Lock()
	for name, manifest := range runningServices {
		if name == serviceName {
			// We don't want this work to be undone by event monitoring
			manifest.AutoRestart = false

			// Updating available mem and cpu shares done by event monitor
			err := StopContainer(manifest.ServiceName, false)
			if err != nil {
                olog <- SLM{serviceName, "failed to stop container"}
				fmt.Println("ERROR IN CONTAINER: ", err)
			} else {
                olog <- SLM{serviceName, "container stopped"}
				fmt.Println("Container stopped successfully")
			}
		}
	}
	runningSvcsLock.Unlock()
}

func handleConfig(m *bw2.SimpleMessage) {
    var config *objects.SvcConfig

	defer func() {
		r := recover()
		if r != nil {
            var tag string
            if config != nil {
                tag = config.ServiceName
            } else {
                tag = "meta"
            }
			olog <- SLM{Service: tag, Message: fmt.Sprintf("Failed to launch service: %+v", r)}
		}
	}()

	cfgPo, ok := m.GetOnePODF(bw2.PODFSpawnpointConfig).(bw2.YAMLPayloadObject)
	if !ok {
		return
	}

	config = &objects.SvcConfig{}
	err := cfgPo.ValueInto(config)
	if err != nil {
		panic(err)
	}

	rawMem := config.MemAlloc
	memAlloc, err := parseMemAlloc(rawMem)
	if err != nil {
		panic(err)
	}

	econtents, err := base64.StdEncoding.DecodeString(config.Entity)
	if err != nil {
		panic(err)
	}

	buildcontents, err := constructBuildContents(config)
	if err != nil {
		panic(err)
	}

	// Check if Spawnpoint has sufficient resources. If not, reject configuration
	availLock.Lock()
	defer availLock.Unlock()
	if memAlloc > availableMem {
		err = fmt.Errorf("Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
			availableMem, memAlloc)
		panic(err)
	} else if config.CpuShares > availableCpuShares {
		err = fmt.Errorf("Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
			availableCpuShares, config.CpuShares)
		panic(err)
	} else {
		availableMem -= memAlloc
		availableCpuShares -= config.CpuShares
	}

	mf := Manifest{
		ServiceName:   config.ServiceName,
		Entity:        econtents,
		ContainerType: config.Container,
		MemAlloc:      memAlloc,
		CpuShares:     config.CpuShares,
		Build:         buildcontents,
		Run:           config.Run,
		AutoRestart:   config.AutoRestart,
	}

	runningSvcsLock.Lock()
	runningServices[config.ServiceName] = &mf
	runningSvcsLock.Unlock()

	// Automatically start the new service. This may change in the future...
	restartService(config.ServiceName, true)
}

func parseMemAlloc(alloc string) (uint64, error) {
	if alloc == "" {
		return 0, errors.New("No memory allocation in config")
	}
	suffix := alloc[len(alloc)-1:]
	memAlloc, err := strconv.ParseUint(alloc[:len(alloc)-1], 0, 64)
	if err != nil {
		return 0, err
	}

	if suffix == "G" || suffix == "g" {
		memAlloc *= 1024
	} else if suffix != "M" && suffix != "m" {
		err = errors.New("Memory allocation amount must be in units of M or G")
		return 0, err
	}

	return memAlloc, nil
}

func constructBuildContents(config *objects.SvcConfig) ([]string, error) {
	rawbuildcontents := strings.Split(config.Build, "\n")
	buildcontents := make([]string, len(rawbuildcontents)+5)
	sourceparts := strings.SplitN(config.Source, "+", 2)
	switch sourceparts[0] {
	case "git":
		buildcontents[0] = "RUN git clone " + sourceparts[1] + " /srv/target"
	default:
		err := errors.New("Unknown source type")
		return nil, err
	}

	parmsString := ""
	for k, v := range config.Params {
		parmsString += k + ": " + v + "\n"
	}
	parms64 := base64.StdEncoding.EncodeToString([]byte(parmsString))

	buildcontents[1] = "WORKDIR /srv/target"
	buildcontents[2] = "RUN echo " + config.Entity + " | base64 --decode > entity.key"
	if config.AptRequires != "" {
		buildcontents[3] = "RUN apt-get update && apt-get install -y " + config.AptRequires
	} else {
		buildcontents[3] = "RUN echo 'no apt-requires'"
	}
	buildcontents[4] = "RUN echo " + parms64 + " | base64 --decode > params.yml"
	for idx, b := range rawbuildcontents {
		buildcontents[idx+5] = "RUN " + b
	}

	return buildcontents, nil
}

func monitorDockerEvents(ec *chan *docker.APIEvents) {
	for event := range *ec {
		if event.Action == "die" {
			runningSvcsLock.Lock()
			for name, manifest := range runningServices {
				// Container can be nil when service is registered but not yet started
				if manifest.Container != nil && manifest.Container.Raw.ID == event.Actor.ID {
					olog <- SLM{name, "Container stopped"}

					if manifest.AutoRestart {
						cnt, err := RestartContainer(manifest, false)
						if err != nil {
                            olog <- SLM{name, "Auto restart failed"}
							fmt.Println("ERROR IN CONTAINER: ", err)
							delete(runningServices, name)
							availLock.Lock()
							availableCpuShares += manifest.CpuShares
							availableMem += manifest.MemAlloc
							availLock.Unlock()
						} else {
                            olog <- SLM{name, "Auto restart successful!"}
							fmt.Println("Container restart ok")
							manifest.Container = cnt
						}
					} else {
						// Still need to update memory and cpu availability
						availLock.Lock()
						availableCpuShares += manifest.CpuShares
						availableMem += manifest.MemAlloc
						availLock.Unlock()
					}
				}
			}
			runningSvcsLock.Unlock()
		}
	}
}
