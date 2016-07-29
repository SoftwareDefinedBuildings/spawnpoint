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
	"github.com/mgutz/ansi"

	docker "github.com/fsouza/go-dockerclient"
	bw2 "gopkg.in/immesys/bw2bind.v5"
	yaml "gopkg.in/yaml.v2"
)

var bwClient *bw2.BW2Client
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

var eventCh chan *docker.APIEvents

const heartbeatPeriod = 5
const persistManifestPeriod = 10

type SLM struct {
	Service string
	Message string
}

func main() {
	app := cli.NewApp()
	app.Name = "spawnd"
	app.Usage = "Run a Spawnpoint Daemon"
	app.Version = objects.SpawnpointVersion

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

	config := DaemonConfig{}
	err = yaml.Unmarshal(configContents, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
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

func actionRun(c *cli.Context) error {
	runningServices = make(map[string]*Manifest)
	olog = make(chan SLM, 100)

	var err error
	cfg, err = readConfigFromFile(c.String("config"))
	if err != nil {
		msg := "Config file error: " + err.Error()
		fmt.Println(msg)
		return errors.New(msg)
	}

	totalCPUShares = cfg.CPUShares
	availableCPUShares = int64(totalCPUShares)
	rawMem := cfg.MemAlloc
	totalMem, err = parseMemAlloc(rawMem)
	if err != nil {
		msg := "Invalid Spawnpoint memory allocation: " + rawMem
		fmt.Println(msg)
		return errors.New(msg)
	}
	availableMem = int64(totalMem)

	bwClient, err = initializeBosswave()
	if err != nil {
		msg := "Failed to initialize Bosswave router: " + err.Error()
		fmt.Println(msg)
		return errors.New(msg)
	}

	// Start docker connection
	eventCh, err = ConnectDocker()
	if err != nil {
		msg := "Failed to connect to Docker: " + err.Error()
		fmt.Println(msg)
		return errors.New(msg)
	}
	go monitorDockerEvents(&eventCh)

	// If possible, recover state from persistence file
	runningServices = make(map[string]*Manifest)
	recoverPreviousState()

	newCfgCh := bwClient.SubscribeOrExit(&bw2.SubscribeParams{URI: uris.SlotPath(cfg.Path, "config")})
	restartCh := bwClient.SubscribeOrExit(&bw2.SubscribeParams{URI: uris.SlotPath(cfg.Path, "restart")})
	stopCh := bwClient.SubscribeOrExit(&bw2.SubscribeParams{URI: uris.SlotPath(cfg.Path, "stop")})

	go doOlog()
	go heartbeat()
	go persistManifests()
	fmt.Printf("Spawnpoint Daemon - Version %s%s%s\n", ansi.ColorCode("cyan+b"),
		objects.SpawnpointVersion, ansi.ColorCode("reset"))

	for {
		select {
		case cfg := <-newCfgCh:
			handleConfig(cfg)

		case restartMsg := <-restartCh:
			namePo := restartMsg.GetOnePODF(bw2.PODFString)
			if namePo != nil {
				runningSvcsLock.Lock()
				mfst, ok := runningServices[string(namePo.GetContents())]
				runningSvcsLock.Unlock()

				if ok {
					if mfst.AutoRestart {
						// Stop the container and let auto restart do the work for us
						StopContainer(mfst.ServiceName, false)
					} else {
						go restartService(mfst, false)
					}
				}
			}

		case stopMsg := <-stopCh:
			namePo := stopMsg.GetOnePODF(bw2.PODFString)
			if namePo != nil {
				stopService(string(namePo.GetContents()))
			}
		}
	}
}

func doOlog() {
	for msg := range olog {
		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, objects.SPLog{
			Time:     time.Now().UnixNano(),
			SPAlias:  cfg.Alias,
			Service:  msg.Service,
			Contents: msg.Message,
		})
		if err != nil {
			panic(err)
		}

		bwClient.PublishOrExit(&bw2.PublishParams{
			URI:            uris.ServiceSignalPath(cfg.Path, msg.Service, "log"),
			PayloadObjects: []bw2.PayloadObject{po},
		})
	}
}

func heartbeat() {
	for {
		// Send heartbeat for spawnpoint
		availLock.Lock()
		mem := availableMem
		shares := availableCPUShares
		availLock.Unlock()

		fmt.Printf("Memory: %v, CPU Shares: %v\n", mem, shares)
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
			panic(err)
		} else {
			hburi := uris.SignalPath(cfg.Path, "heartbeat")
			bwClient.PublishOrExit(&bw2.PublishParams{
				URI:            hburi,
				Persist:        true,
				PayloadObjects: []bw2.PayloadObject{po},
			})
		}

		time.Sleep(heartbeatPeriod * time.Second)
	}
}

func svcHeartbeat(svcname string, statCh *chan *docker.Stats) {
	lastEmitted := time.Now()
	lastCPUPercentage := 0.0
	for stats := range *statCh {
		if stats.Read.Sub(lastEmitted).Seconds() > heartbeatPeriod {
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
				panic(err)
			}

			bwClient.PublishOrExit(&bw2.PublishParams{
				URI:            hburi,
				Persist:        true,
				PayloadObjects: []bw2.PayloadObject{po},
			})

			lastEmitted = time.Now()
		}
	}
}

func restartService(mfst *Manifest, initialBoot bool) {
	if initialBoot {
		olog <- SLM{mfst.ServiceName, "booting service"}
	} else {
		olog <- SLM{mfst.ServiceName, "attempting restart"}
	}

	if mfst.logger == nil {
		logger := NewLogger(bwClient, cfg.Path, cfg.Alias, mfst.ServiceName)
		mfst.logger = logger
	}

	cnt, err := RestartContainer(mfst, cfg.LocalRouter, initialBoot)
	if err != nil {
		msg := fmt.Sprintf("Container (re)start failed: %v", err)
		olog <- SLM{mfst.ServiceName, msg}
		fmt.Println(mfst.ServiceName, "::", msg)

		availLock.Lock()
		availableMem += int64(mfst.MemAlloc)
		availableCPUShares += int64(mfst.CPUShares)
		availLock.Unlock()

		runningSvcsLock.Lock()
		delete(runningServices, mfst.ServiceName)
		runningSvcsLock.Unlock()
	} else {
		msg := "Container (re)start successful"
		olog <- SLM{mfst.ServiceName, msg}
		fmt.Println(mfst.ServiceName, "::", msg)
		mfst.Container = cnt

		if initialBoot {
			runningSvcsLock.Lock()
			runningServices[mfst.ServiceName] = mfst
			runningSvcsLock.Unlock()
		}
		go svcHeartbeat(mfst.ServiceName, cnt.StatChan)
	}
}

func stopService(serviceName string) {
	olog <- SLM{serviceName, "attempting to stop container"}
	runningSvcsLock.Lock()
	for name, manifest := range runningServices {
		if name == serviceName {
			// We don't want this work to be undone by event monitoring
			manifest.AutoRestart = false

			// Updating available mem and cpu shares done by event monitor
			err := StopContainer(manifest.ServiceName, true)
			if err != nil {
				msg := "Failed to stop container: " + err.Error()
				olog <- SLM{serviceName, msg}
				fmt.Println(msg)
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
	err := cfgPo.ValueInto(&config)
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
	var restartWaitDur time.Duration
	if config.RestartInt != "" {
		restartWaitDur, err = time.ParseDuration(config.RestartInt)
		if err != nil {
			panic(err)
		}
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

	mf := Manifest{
		ServiceName:   config.ServiceName,
		Entity:        econtents,
		ContainerType: config.Container,
		MemAlloc:      memAlloc,
		CPUShares:     config.CPUShares,
		Build:         buildcontents,
		Run:           config.Run,
		AutoRestart:   config.AutoRestart,
		RestartInt:    restartWaitDur,
		Volumes:       config.Volumes,
		logger:        NewLogger(bwClient, cfg.Path, cfg.Alias, config.ServiceName),
	}

	go restartService(&mf, true)

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
			var relevantManifest *Manifest
			runningSvcsLock.Lock()
			for _, manifest := range runningServices {
				// Need to shadow to avoid loop reference variable bug
				if manifest.Container.Raw.ID == event.Actor.ID {
					relevantManifest = manifest
				}
			}
			runningSvcsLock.Unlock()

			if relevantManifest != nil {
				olog <- SLM{relevantManifest.ServiceName, "Container stopped"}
				if relevantManifest.AutoRestart {
					go func() {
						if relevantManifest.RestartInt > 0 {
							time.Sleep(relevantManifest.RestartInt)
						}
						restartService(relevantManifest, false)
					}()
				} else {
					runningSvcsLock.Lock()
					delete(runningServices, relevantManifest.ServiceName)
					runningSvcsLock.Unlock()
					// Still need to update memory and cpu availability
					availLock.Lock()
					availableCPUShares += int64(relevantManifest.CPUShares)
					availableMem += int64(relevantManifest.MemAlloc)
					availLock.Unlock()
				}
			}
		}
	}
}

func persistManifests() {
	for {
		manifests := make([]Manifest, len(runningServices))
		i := 0

		runningSvcsLock.Lock()
		for _, mfstPtr := range runningServices {
			// Make a deep copy
			manifests[i] = Manifest{
				ServiceName:   mfstPtr.ServiceName,
				Entity:        mfstPtr.Entity,
				ContainerType: mfstPtr.ContainerType,
				MemAlloc:      mfstPtr.MemAlloc,
				CPUShares:     mfstPtr.CPUShares,
				Build:         mfstPtr.Build,
				Run:           mfstPtr.Run,
				AutoRestart:   mfstPtr.AutoRestart,
				RestartInt:    mfstPtr.RestartInt,
				Volumes:       mfstPtr.Volumes,
			}
		}
		runningSvcsLock.Unlock()

		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumMsgPack, manifests)
		if err != nil {
			fmt.Println("Failed to marshal manifests for persistence")
		} else {
			bwClient.Publish(&bw2.PublishParams{
				URI:            uris.SignalPath(cfg.Path, "manifests"),
				Persist:        true,
				PayloadObjects: []bw2.PayloadObject{po},
			})
		}

		time.Sleep(persistManifestPeriod * time.Second)
	}
}

func recoverPreviousState() {
	mfstMsg := bwClient.QueryOneOrExit(&bw2.QueryParams{
		URI: uris.SignalPath(cfg.Path, "manifests"),
	})
	if mfstMsg == nil {
		return
	}

	msgPackPo := mfstMsg.GetOnePODF(bw2.PODFMsgPack)
	if msgPackPo == nil {
		fmt.Println("Error: Persisted manifests did not contain msgpack PO")
		return
	}

	var priorManifests []Manifest
	if err := msgPackPo.(bw2.MsgPackPayloadObject).ValueInto(&priorManifests); err != nil {
		fmt.Printf("Failed to unmarshal persistence data: %v\n", err)
		return
	}

	knownContainers, err := GetSpawnedContainers()
	if err != nil {
		fmt.Printf("Failed to retrieve container info: %v\n", err)
		return
	}

	// Note that we do not have to worry about memory or CPU consumption
	// Previously running instance of spawnd enforced limits for us
	newManifests := make(map[string]*Manifest)
	for _, mfst := range priorManifests {
		containerInfo, ok := knownContainers[mfst.ServiceName]
		if ok {
			if containerInfo.Raw.State.Running || mfst.AutoRestart {
				if containerInfo.Raw.State.Running {
					// Container continued running after spawnd terminated, reclaim ownership
					logger := NewLogger(bwClient, cfg.Path, cfg.Alias, mfst.ServiceName)
					mfst.logger = logger
					mfst.Container = ReclaimContainer(&mfst, containerInfo.Raw)
				} else {
					// Container has stopped, so try to restart it
					restartService(&mfst, false)
				}

				newManifests[mfst.ServiceName] = &mfst
				go svcHeartbeat(mfst.ServiceName, mfst.Container.StatChan)
				availLock.Lock()
				availableMem -= int64(mfst.MemAlloc)
				availableCPUShares -= int64(mfst.CPUShares)
				availLock.Unlock()
			}
		}
	}

	runningSvcsLock.Lock()
	runningServices = newManifests
	runningSvcsLock.Unlock()
}
