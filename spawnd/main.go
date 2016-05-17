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

	docker "github.com/fsouza/go-dockerclient"
	bw2 "gopkg.in/immesys/bw2bind.v1"
	yaml "gopkg.in/yaml.v2"
)

var bwClient *bw2.BW2Client
var PrimaryAccessChain string
var Cfg DaemonConfig
var olog chan SLM

var (
	totalMem           uint64 // Memory dedicated to Spawnpoint, in MiB
	totalCpuShares     uint64 // CPU shares for Spawnpoint, 1024 per core
	availableMem       uint64
	availableCpuShares uint64
	availLock          sync.Mutex
)

var (
	runningServices map[string]Manifest
	runningSvcsLock sync.Mutex
)

var eventCh chan *docker.APIEvents

const HEARTBEAT_PERIOD = 5

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

func initializeBosswave() (*bw2.BW2Client, string, error) {
	if Cfg.LocalRouter == "" {
		Cfg.LocalRouter = "127.0.0.1:28589"
	}

	client, err := bw2.Connect(Cfg.LocalRouter)
	if err != nil {
		return nil, "", err
	}

	us, err := client.SetEntityFile(Cfg.Entity)
	if err != nil {
		return nil, "", err
	}

	pac, err := client.BuildAnyChain(path("*"), "PC", us)
	if err != nil {
		return nil, "", err
	}

	return client, pac.Hash, nil
}

func main() {
	Cfg, err := readConfigFromFile("config.yml")
	if err != nil {
		fmt.Println("Config file error:", err)
		os.Exit(1)
	}

	totalCpuShares = Cfg.CpuShares
	availableCpuShares = totalCpuShares
	rawMem := Cfg.MemAlloc
	memAlloc, err := parseMemAlloc(rawMem)
	if err != nil {
		fmt.Println("Invalid Spawnpoint memory allocation:", err)
		os.Exit(1)
	}
	totalMem = memAlloc
	availableMem = totalMem

	bwClient, PrimaryAccessChain, err = initializeBosswave()
	if err != nil {
		fmt.Println("Failed to connect to Bosswave router and establish permissions")
		os.Exit(1)
	} else {
		fmt.Println("Connected to router and obtained permissions")
	}

	// Start docker connection
	eventCh, err = ConnectDocker()
	if err != nil {
		fmt.Println("Could not connect to Docker:", err)
		os.Exit(1)
	}
	go monitorDockerEvents(&eventCh)

	newconfigs, err := bwClient.Subscribe(&bw2.SubscribeParams{
		URI:                path("ctl/cfg"),
		PrimaryAccessChain: PrimaryAccessChain,
		ElaboratePAC:       bw2.ElaborateFull,
	})
	if err != nil {
		fmt.Println("Could not subscribe to config URI: ", err)
		os.Exit(1)
	}

	restart, err := bwClient.Subscribe(&bw2.SubscribeParams{
		URI:                path("ctl/restart"),
		PrimaryAccessChain: PrimaryAccessChain,
		ElaboratePAC:       bw2.ElaborateFull,
	})
	if err != nil {
		fmt.Println("Could not subscribe to restart URI: ", err)
		os.Exit(1)
	}

	stop, err := bwClient.Subscribe(&bw2.SubscribeParams{
		URI:                path("ctl/stop"),
		PrimaryAccessChain: PrimaryAccessChain,
		ElaboratePAC:       bw2.ElaborateFull,
	})
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
				restartService(svcName, true)
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
		URI:                path("info/spawn/" + svcname + "/log"),
		PrimaryAccessChain: PrimaryAccessChain,
		ElaboratePAC:       bw2.ElaborateFull,
		Persist:            true,
		PayloadObjects:     POs,
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
			SPLog{
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
				SPLog{
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

		msg := SpawnPointHb{
			Cfg.Alias,
			time.Now().Format(time.RFC3339),
			mem,
			shares,
		}
		po, err := bw2.CreateYAMLPayloadObject(bw2.FromDotForm(SpawnPointHeartbeatType), msg)
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
		cnt, err := RestartContainer(&manifest, true)
		if err != nil {
			fmt.Println("ERROR IN CONTAINER: ", err)
		} else {
			fmt.Println("Container started ok")
			manifest.Container = cnt
		}
	}
	runningSvcsLock.Unlock()
}

func restartService(serviceName string, fetchNewImage bool) {
	olog <- SLM{serviceName, "attempting restart"}
	runningSvcsLock.Lock()
	for name, manifest := range runningServices {
		if name == serviceName {
			cnt, err := RestartContainer(&manifest, fetchNewImage)
			if err != nil {
				fmt.Println("ERROR IN CONTAINER: ", err)
			} else {
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
				fmt.Println("ERROR IN CONTAINER: ", err)
			} else {
				fmt.Println("Container stopped successfully")
			}
		}
	}
	runningSvcsLock.Unlock()
}

func handleConfig(m *bw2.SimpleMessage) {
	defer func() {
		r := recover()
		if r != nil {
			olog <- SLM{Service: "meta", Message: "Error initializing service: " + fmt.Sprintf("%+v", r)}
		}
	}()

	cfg, ok := m.GetOnePODF(bw2.PODFSpawnpointConfig).(bw2.YAMLPayloadObject)
	if !ok {
		return
	}
	toplevel := make(map[string]interface{})
	err := cfg.ValueInto(toplevel)
	if err != nil {
		panic(err)
	}

	for svcname, val := range toplevel {
		svc := val.(map[interface{}]interface{})
		rawMem, ok := svc["memAlloc"].(string)
		if !ok {
			panic(errors.New("bad memAlloc option"))
		}
		memAlloc, err := parseMemAlloc(rawMem)
		if err != nil {
			panic(err)
		}
		rawCpuShares, ok := svc["cpuShares"].(string)
		if !ok {
			panic(errors.New("bad cpuShares option"))
		}
		cpuShares, err := strconv.ParseUint(rawCpuShares, 0, 64)
		if err != nil {
			panic(err)
		}
		econtents, err := base64.StdEncoding.DecodeString(svc["entity"].(string))
		if err != nil {
			panic(err)
		}

		rawbuildcontents := strings.Split(svc["build"].(string), "\n")
		buildcontents := make([]string, len(rawbuildcontents)+5)
		sourceparts := strings.SplitN(svc["source"].(string), "+", 2)
		switch sourceparts[0] {
		case "git":
			buildcontents[0] = "RUN git clone " + sourceparts[1] + " /srv/target"
		default:
			fmt.Println("Unknown source type")
			continue
		}
		parmsstring, err := yaml.Marshal(svc["params"])
		if err != nil {
			panic(err)
		}
		parms64 := base64.StdEncoding.EncodeToString(parmsstring)

		buildcontents[1] = "WORKDIR /srv/target"
		buildcontents[2] = "RUN echo " + svc["entity"].(string) + " | base64 --decode > entity.key"
		if svc["aptrequires"] != nil && svc["aptrequires"].(string) != "" {
			buildcontents[3] = "RUN apt-get update && apt-get install -y " + svc["aptrequires"].(string)
		} else {
			buildcontents[3] = "RUN echo 'no apt-requires'"
		}
		buildcontents[4] = "RUN echo " + parms64 + " | base64 --decode > params.yml"
		for idx, b := range rawbuildcontents {
			buildcontents[idx+5] = "RUN " + b
		}
		run := make([]string, len(svc["run"].([]interface{})))
		for idx, val := range svc["run"].([]interface{}) {
			run[idx] = val.(string)
		}

		// Check if Spawnpoint has sufficient resources. If not, reject configuration
		availLock.Lock()
		defer availLock.Unlock()
		if memAlloc > availableMem {
			err = fmt.Errorf("Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
				availableMem, memAlloc)
			panic(err)
		} else if cpuShares > availableCpuShares {
			err = fmt.Errorf("Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
				availableCpuShares, cpuShares)
			panic(err)
		} else {
			availableMem -= memAlloc
			availableCpuShares -= cpuShares
		}

		mf := Manifest{
			ServiceName:   svcname,
			Entity:        econtents,
			ContainerType: svc["container"].(string),
			MemAlloc:      memAlloc,
			CpuShares:     cpuShares,
			Build:         buildcontents,
			Run:           run,
			AutoRestart:   svc["autoRestart"].(bool),
		}
		runningSvcsLock.Lock()
		runningServices[svcname] = mf
		runningSvcsLock.Unlock()
	}
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

func monitorDockerEvents(ec *chan *docker.APIEvents) {
	for event := range *ec {
		if event.Action == "die" {
			runningSvcsLock.Lock()
			for name, manifest := range runningServices {
				if manifest.Container.Raw.ID == event.Actor.ID {
					olog <- SLM{name, "Container stopped"}

					if manifest.AutoRestart {
						cnt, err := RestartContainer(&manifest, false)
						if err != nil {
							fmt.Println("ERROR IN CONTAINER: ", err)
							delete(runningServices, name)
							availLock.Lock()
							availableCpuShares += manifest.CpuShares
							availableMem += manifest.MemAlloc
							availLock.Unlock()
						} else {
							fmt.Println("Container restart ok")
							manifest.Container = cnt
						}
					} else {
						// Still need to update memory and cpu availability
						availLock.Lock()
						availableCpuShares += manifest.CpuShares
						availableMem += manifest.MemAlloc
					}
				}
			}
			runningSvcsLock.Unlock()
		}
	}
}
