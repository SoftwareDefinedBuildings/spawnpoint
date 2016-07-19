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

	"github.com/codegangsta/cli"
	"github.com/immesys/spawnpoint/objects"
	"github.com/immesys/spawnpoint/uris"

	docker "github.com/fsouza/go-dockerclient"
	bw2 "gopkg.in/immesys/bw2bind.v5"
	yaml "gopkg.in/yaml.v2"
)

var bwClient *bw2.BW2Client
var primaryAccessChain string
var cfg *DaemonConfig
var olog chan SLM

var (
	totalMem           uint64 // Memory dedicated to Spawnpoint, in MiB
	totalCPUShares     uint64 // CPU shares for Spawnpoint, 1024 per core
	availableMem       int64
	availableCPUShares int64
	availLock          sync.Mutex
)

var (
	runningServices map[string]*Manifest
	runningSvcsLock sync.Mutex
)

var svcSpawnCh chan *Manifest

var eventCh chan *docker.APIEvents

const HeartbeatPeriod = 5

type SLM struct {
	Service string
	Message string
}

func main() {
	app := cli.NewApp()
	app.Name = "spawnd"
	app.Usage = "Run a Spawnpoint Daemon"
	app.Version = "0.0.2"

	app.Commands = []cli.Command{
		{
			Name:   "run",
			Usage:  "Run a spawnpoint daemon",
			Action: actionRun,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "config, c",
					Usage: "Specify a configuration file for the daemon",
					Value: "config.yml",
				},
			},
		},
	}

	app.Run(os.Args)
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
	}
	return config, nil
}

func initializeBosswave() (*bw2.BW2Client, error) {
	client, err := bw2.Connect(cfg.LocalRouter)
	if err != nil {
		return nil, err
	}

	_, err = client.SetEntityFile(cfg.Entity)
	if err != nil {
		return nil, err
	}

	client.OverrideAutoChainTo(true)
	return client, nil
}

func actionRun(c *cli.Context) {
	runningServices = make(map[string]*Manifest)
	svcSpawnCh = make(chan *Manifest, 20)
	olog = make(chan SLM, 100)

	var err error
	cfg, err = readConfigFromFile(c.String("config"))
	if err != nil {
		fmt.Println("Config file error:", err)
		os.Exit(1)
	}

	totalCPUShares = cfg.CPUShares
	availableCPUShares = int64(totalCPUShares)
	rawMem := cfg.MemAlloc
	totalMem, err = parseMemAlloc(rawMem)
	if err != nil {
		fmt.Println("Invalid Spawnpoint memory allocation:", err)
		os.Exit(1)
	}
	availableMem = int64(totalMem)

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

	newconfigs, err := bwClient.Subscribe(&bw2.SubscribeParams{URI: uris.SlotPath(cfg.Path, "config")})
	if err != nil {
		fmt.Println("Could not subscribe to config URI: ", err)
		os.Exit(1)
	}

	restart, err := bwClient.Subscribe(&bw2.SubscribeParams{URI: uris.SlotPath(cfg.Path, "restart")})
	if err != nil {
		fmt.Println("Could not subscribe to restart URI: ", err)
		os.Exit(1)
	}

	stop, err := bwClient.Subscribe(&bw2.SubscribeParams{URI: uris.SlotPath(cfg.Path, "stop")})
	if err != nil {
		fmt.Println("Could not subscribe to stop URI: ", err)
		os.Exit(1)
	}

	go doOlog()
	go heartbeat()
	go doSvcSpawn()
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
				restartService(svcName)
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
		URI:            uris.ServiceSignalPath(cfg.Path, svcname, "log"),
		PayloadObjects: POs,
	})
	if err != nil {
		fmt.Println("Error publishing log: ", err)
	}
}

func doOlog() {
	for {
		POs := make(map[string][]bw2.PayloadObject)
		msg := <-olog
		po1, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog,
			objects.SPLog{
				Time:     time.Now().UnixNano(),
				SPAlias:  cfg.Alias,
				Service:  msg.Service,
				Contents: msg.Message,
			})
		if err != nil {
			panic(err)
		}
		POs[msg.Service] = []bw2.PayloadObject{po1}

		//Opportunistically add another few messages if they are in the channel
		for i := 0; i < len(olog); i++ {
			msgN := <-olog
			poN, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog,
				objects.SPLog{
					Time:     time.Now().UnixNano(),
					SPAlias:  cfg.Alias,
					Service:  msgN.Service,
					Contents: msgN.Message,
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
		// Send heartbeat for spawnpoint
		availLock.Lock()
		mem := availableMem
		shares := availableCPUShares
		availLock.Unlock()

		fmt.Printf("mem: %v, shares: %v\n", mem, shares)
		msg := objects.SpawnPointHb{
			Alias:              cfg.Alias,
			Time:               time.Now().UnixNano(),
			TotalMem:           totalMem,
			TotalCPUShares:     totalCPUShares,
			AvailableMem:       mem,
			AvailableCPUShares: shares,
		}
		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointHeartbeat, msg)
		if err != nil {
			fmt.Println("Failed to create spawnpoint heartbeat message")
			time.Sleep(HeartbeatPeriod * time.Second)
			continue
		}

		hburi := uris.SignalPath(cfg.Path, "heartbeat")
		err = bwClient.Publish(&bw2.PublishParams{
			URI:            hburi,
			Persist:        true,
			PayloadObjects: []bw2.PayloadObject{po},
		})
		if err != nil {
			fmt.Println("Failed to publish spawnpoint heartbeat message")
		}

		time.Sleep(HeartbeatPeriod * time.Second)
	}
}

func svcHeartbeat(svcname string, statCh *chan *docker.Stats) {
	lastEmitted := time.Now()
	lastCPUPercentage := 0.0
	for stats := range *statCh {
		if stats.Read.Sub(lastEmitted).Seconds() > HeartbeatPeriod {
			runningSvcsLock.Lock()
			manifest, ok := runningServices[svcname]
			runningSvcsLock.Unlock()
			if !ok {
				return
			}

			mbRead := 0.0
			mbWritten := 0.0
			for _, ioStats := range stats.BlkioStats.IOServiceBytesRecursive {
				if ioStats.Op == "Read" {
					mbRead += float64(ioStats.Value)
				} else if ioStats.Op == "Write" {
					mbWritten += float64(ioStats.Value)
				}
			}
			mbRead /= (1024 * 1024)
			mbWritten /= (1024 * 1024)

			// Based on Docker's 'calculateCPUPercent' function
			containerCPUDelta := float64(stats.CPUStats.CPUUsage.TotalUsage -
				stats.PreCPUStats.CPUUsage.TotalUsage)
			systemCPUDelta := float64(stats.CPUStats.SystemCPUUsage -
				stats.PreCPUStats.SystemCPUUsage)
			if systemCPUDelta > 0.0 {
				numCores := float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
				lastCPUPercentage = (containerCPUDelta / systemCPUDelta) * numCores * 100.0
			}

			hburi := uris.ServiceSignalPath(cfg.Path, svcname, "heartbeat")
			msg := objects.SpawnpointSvcHb{
				SpawnpointURI: cfg.Path,
				Name:          svcname,
				Time:          time.Now().UnixNano(),
				MemAlloc:      manifest.MemAlloc,
				CPUShares:     manifest.CPUShares,
				MemUsage:      float64(stats.MemoryStats.Usage) / (1024 * 1024),
				NetworkRx:     float64(stats.Network.RxBytes) / (1024 * 1024),
				NetworkTx:     float64(stats.Network.TxBytes) / (1024 * 1024),
				MbRead:        mbRead,
				MbWritten:     mbWritten,
				CPUPercent:    lastCPUPercentage,
			}

			po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointSvcHb, msg)
			if err != nil {
				fmt.Println("Failed to create heartbeat message for service ", svcname)
				break
			}

			err = bwClient.Publish(&bw2.PublishParams{
				URI:            hburi,
				Persist:        true,
				PayloadObjects: []bw2.PayloadObject{po},
			})
			if err != nil {
				fmt.Println("Failed to publish heartbeat message for service", svcname)
				break
			}

			lastEmitted = time.Now()
		}
	}
}

func respawn() {
	olog <- SLM{"meta", "doing respawn"}
	runningSvcsLock.Lock()
	for svcName, manifest := range runningServices {
		cnt, err := RestartContainer(manifest, cfg.LocalRouter, true)
		if err != nil {
			msg := fmt.Sprintf("Container restart failed: %v", err)
			olog <- SLM{svcName, msg}
			fmt.Println(svcName, "::", msg)
		} else {
			olog <- SLM{svcName, "Container start ok"}
			fmt.Println("Container started ok")
			manifest.Container = cnt
		}
	}
	runningSvcsLock.Unlock()
}

func restartService(serviceName string) {
	olog <- SLM{serviceName, "attempting restart"}
	runningSvcsLock.Lock()
	manifest, ok := runningServices[serviceName]
	if ok {
		cnt, err := RestartContainer(manifest, cfg.LocalRouter, false)
		if err != nil {
			msg := fmt.Sprintf("Container restart failed: %v", err)
			olog <- SLM{serviceName, msg}
			fmt.Println(serviceName, "::", msg)
		} else {
			olog <- SLM{serviceName, "restart successful!"}
			fmt.Println("Container restarted ok")
			manifest.Container = cnt
			go svcHeartbeat(serviceName, cnt.StatChan)
		}
	} else {
		olog <- SLM{serviceName, "service does not exist"}
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
			err := StopContainer(manifest.ServiceName)
			if err != nil {
				olog <- SLM{serviceName, "failed to stop container"}
				fmt.Println("ERROR IN CONTAINER: ", err)
			} else {
				// Log message will be output by docker event monitor
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

	fileIncludePo := m.GetOnePODF(bw2.PODFString)
	var fileIncludeEnc string
	if fileIncludePo != nil {
		fileIncludeEnc = string(fileIncludePo.GetContents())
	}
	buildcontents, err := constructBuildContents(config, fileIncludeEnc)
	if err != nil {
		panic(err)
	}

	// Previous version of service could already be running
	runningSvcsLock.Lock()
	existingManifest, ok := runningServices[config.ServiceName]
	runningSvcsLock.Unlock()
	previousMem := int64(0)
	previousCPU := int64(0)
	if ok {
		olog <- SLM{config.ServiceName, "Found instance of service already running"}
		previousMem = int64(existingManifest.MemAlloc)
		previousCPU = int64(existingManifest.CPUShares)
	}

	// Check if Spawnpoint has sufficient resources. If not, reject configuration
	availLock.Lock()
	defer availLock.Unlock()
	if int64(memAlloc) > (availableMem + int64(previousMem)) {
		err = fmt.Errorf("Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
			availableMem+previousMem, memAlloc)
		panic(err)
	} else if int64(config.CPUShares) > (availableCPUShares + int64(previousCPU)) {
		err = fmt.Errorf("Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
			availableCPUShares+previousCPU, config.CPUShares)
		panic(err)
	} else {
		availableMem -= int64(memAlloc)
		availableCPUShares -= int64(config.CPUShares)
	}

	// Remove previous service version if necessary
	if ok {
		olog <- SLM{config.ServiceName, "Removing old version"}
		stopService(config.ServiceName)
	}

	logger, err := NewLogger(bwClient, cfg.Path, cfg.Alias, config.ServiceName)
	if err != nil {
		panic(err)
	}

	mf := Manifest{
		ServiceName:   config.ServiceName,
		Entity:        econtents,
		ContainerType: config.Container,
		MemAlloc:      memAlloc,
		CPUShares:     config.CPUShares,
		Build:         buildcontents,
		Run:           config.Run,
		AutoRestart:   config.AutoRestart,
		Volumes:       config.Volumes,
		logger:        logger,
	}

	svcSpawnCh <- &mf
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

func constructBuildContents(config *objects.SvcConfig, includeTarEnc string) ([]string, error) {
	var buildcontents []string
	next := 0
	if config.Source != "" {
		buildcontents = make([]string, len(config.Build)+5)
		sourceparts := strings.SplitN(config.Source, "+", 2)
		switch sourceparts[0] {
		case "git":
			buildcontents[next] = "RUN git clone " + sourceparts[1] + " /srv/spawnpoint"
			next++
		default:
			err := errors.New("Unknown source type")
			return nil, err
		}
	} else {
		buildcontents = make([]string, len(config.Build)+4)
	}

	buildcontents[next] = "WORKDIR /srv/spawnpoint"
	next++
	buildcontents[next] = "RUN echo " + config.Entity + " | base64 --decode > entity.key"
	next++
	if config.AptRequires != "" {
		buildcontents[next] = "RUN apt-get update && apt-get install -y " + config.AptRequires
		next++
	} else {
		buildcontents[next] = "RUN echo 'no apt-requires'"
		next++
	}

	if includeTarEnc != "" {
		buildcontents[next] = "RUN echo " + includeTarEnc + " | base64 --decode > include.tar" +
			" && tar -xf include.tar"
		next++
	}

	for _, b := range config.Build {
		buildcontents[next] = "RUN " + b
		next++
	}

	return buildcontents, nil
}

func monitorDockerEvents(ec *chan *docker.APIEvents) {
	for event := range *ec {
		if event.Action == "die" {
			runningSvcsLock.Lock()
			for name, manifest := range runningServices {
				if manifest.Container.Raw.ID == event.Actor.ID {
					olog <- SLM{name, "Container stopped"}

					if manifest.AutoRestart {
						cnt, err := RestartContainer(manifest, cfg.LocalRouter, false)
						if err != nil {
							olog <- SLM{name, "Auto restart failed"}
							fmt.Println("ERROR IN CONTAINER: ", err)
							delete(runningServices, name)
							availLock.Lock()
							availableCPUShares += int64(manifest.CPUShares)
							availableMem += int64(manifest.MemAlloc)
							availLock.Unlock()
						} else {
							olog <- SLM{name, "Auto restart successful!"}
							fmt.Println("Container restart ok")
							manifest.Container = cnt
							go svcHeartbeat(manifest.ServiceName, cnt.StatChan)
						}
					} else {
						delete(runningServices, name)
						// Still need to update memory and cpu availability
						availLock.Lock()
						availableCPUShares += int64(manifest.CPUShares)
						availableMem += int64(manifest.MemAlloc)
						availLock.Unlock()
					}
				}
			}
			runningSvcsLock.Unlock()
		}
	}
}

func doSvcSpawn() {
	for manifest := range svcSpawnCh {
		olog <- SLM{manifest.ServiceName, "starting new service"}
		cnt, err := RestartContainer(manifest, cfg.LocalRouter, true)
		if err != nil {
			msg := fmt.Sprintf("Failed to start new container: %v", err)
			olog <- SLM{manifest.ServiceName, msg}
			fmt.Println(manifest.ServiceName, "::", msg)

			// We have to update resource pool manually
			// Event monitor doesn't know about pending container yet
			availLock.Lock()
			availableMem += int64(manifest.MemAlloc)
			availableCPUShares += int64(manifest.CPUShares)
			availLock.Unlock()
		} else {
			olog <- SLM{manifest.ServiceName, "start successful!"}
			fmt.Println("Container started ok")
			manifest.Container = cnt

			runningSvcsLock.Lock()
			runningServices[manifest.ServiceName] = manifest
			runningSvcsLock.Unlock()

			go svcHeartbeat(manifest.ServiceName, manifest.Container.StatChan)
		}
	}
}
