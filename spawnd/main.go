package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/codegangsta/cli"
	"github.com/immesys/spawnpoint/objects"
	"github.com/mgutz/ansi"

	docker "github.com/fsouza/go-dockerclient"
	bw2 "gopkg.in/immesys/bw2bind.v5"
	yaml "gopkg.in/yaml.v2"
)

var bwClient *bw2.BW2Client
var cfg *DaemonConfig
var olog chan SLM
var spInterface *bw2.Interface

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
const defaultSpawnpointImage = "jhkolb/spawnpoint:amd64"

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
				cli.StringFlag{
					Name:  "metadata, m",
					Usage: "Specify a file containing key/value metadata pairs",
					Value: "",
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

func readMetadataFromFile(fileName string) (*map[string]string, error) {
	mdContents, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	metadata := make(map[string]string)
	err = yaml.Unmarshal(mdContents, &metadata)
	if err != nil {
		return nil, err
	}
	return &metadata, nil
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
		fmt.Println("Config file error", err)
		os.Exit(1)
	}

	totalCPUShares = cfg.CPUShares
	availableCPUShares = int64(totalCPUShares)
	rawMem := cfg.MemAlloc
	totalMem, err = objects.ParseMemAlloc(rawMem)
	if err != nil {
		fmt.Println("Invalid Spawnpoint memory allocation: " + rawMem)
		os.Exit(1)
	}
	availableMem = int64(totalMem)

	bwClient, err = initializeBosswave()
	if err != nil {
		fmt.Println("Failed to initialize Bosswave router:", err)
		os.Exit(1)
	}

	// Register spawnpoint service and interfaces
	spService := bwClient.RegisterService(cfg.Path, "s.spawnpoint")
	spInterface = spService.RegisterInterface("server", "i.spawnpoint")
	spInterface.SubscribeSlot("config", handleConfig)
	spInterface.SubscribeSlot("restart", handleRestart)
	spInterface.SubscribeSlot("stop", handleStop)

	// Set Spawnpoint metadata
	if c.String("metadata") != "" {
		metadata, err := readMetadataFromFile(c.String("metadata"))
		if err != nil {
			fmt.Println("Invalid metadata file:", err)
			os.Exit(1)
		}
		go func() {
			for {
				for mdKey, mdVal := range *metadata {
					spService.SetMetadata(mdKey, mdVal)
				}
				time.Sleep(1 * time.Minute)
			}
		}()
	}

	// Start docker connection
	eventCh, err = ConnectDocker()
	if err != nil {
		fmt.Println("Failed to connect to Docker:", err)
		os.Exit(1)
	}
	go monitorDockerEvents(&eventCh)

	// If possible, recover state from persistence file
	runningServices = make(map[string]*Manifest)
	recoverPreviousState()

	go heartbeat()
	go persistManifests()
	fmt.Printf("Spawnpoint Daemon - Version %s%s%s\n", ansi.ColorCode("cyan+b"),
		objects.SpawnpointVersion, ansi.ColorCode("reset"))

	// Publish outgoing log messages
	for msg := range olog {
		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, objects.SPLogMsg{
			Time:     time.Now().UnixNano(),
			SPAlias:  cfg.Alias,
			Service:  msg.Service,
			Contents: msg.Message,
		})
		if err != nil {
			fmt.Println("Failed to construct log message:", err)
			os.Exit(1)
		}

		if err := spInterface.PublishSignal("log", po); err != nil {
			fmt.Println("Failed to publish log message:", err)
			os.Exit(1)
		}
	}

	// Not reached
	return nil
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
		hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointHeartbeat, msg)
		if err != nil {
			panic(err)
		}

		err = spInterface.PublishSignal("heartbeat", hbPo)
		if err != nil {
			panic(err)
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

			hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointSvcHb, msg)
			if err != nil {
				panic(err)
			}
			err = spInterface.PublishSignal("heartbeat/"+svcname, hbPo)
			if err != nil {
				panic(err)
			}

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

	cnt, err := RestartContainer(mfst, cfg.ContainerRouter, initialBoot)
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

func stopService(mfst *Manifest) {
	olog <- SLM{mfst.ServiceName, "attempting to stop container"}
	// We don't want this work to be undone by event monitoring
	mfst.AutoRestart = false

	// Updating available mem and cpu shares done by event monitor
	err := StopContainer(mfst.ServiceName, true)
	if err != nil {
		msg := "Failed to stop container: " + err.Error()
		olog <- SLM{mfst.ServiceName, msg}
		fmt.Println(msg)
	} else {
		// Log message will be output by docker event monitor
		fmt.Println("Container stopped successfully")
	}
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

	if config.Image == "" {
		config.Image = defaultSpawnpointImage
	}

	rawMem := config.MemAlloc
	memAlloc, err := objects.ParseMemAlloc(rawMem)
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
		stopService(existingManifest)
	}

	mf := Manifest{
		ServiceName: config.ServiceName,
		Entity:      econtents,
		Image:       config.Image,
		MemAlloc:    memAlloc,
		CPUShares:   config.CPUShares,
		Build:       buildcontents,
		Run:         config.Run,
		AutoRestart: config.AutoRestart,
		RestartInt:  restartWaitDur,
		Volumes:     config.Volumes,
		logger:      NewLogger(bwClient, cfg.Path, cfg.Alias, config.ServiceName),
		OverlayNet:  config.OverlayNet,
	}

	go restartService(&mf, true)

}

func handleRestart(msg *bw2.SimpleMessage) {
	namePo := msg.GetOnePODF(bw2.PODFString)
	if namePo != nil {
		svcName := string(namePo.GetContents())
		runningSvcsLock.Lock()
		mfst, ok := runningServices[svcName]
		runningSvcsLock.Unlock()

		if ok {
			if mfst.AutoRestart {
				// Stop the container and let auto restart do the work for us
				StopContainer(mfst.ServiceName, false)
			} else {
				go restartService(mfst, false)
			}
		} else {
			olog <- SLM{Service: svcName, Message: "Service not found"}
		}
	}
}

func handleStop(msg *bw2.SimpleMessage) {
	namePo := msg.GetOnePODF(bw2.PODFString)
	if namePo != nil {
		svcName := string(namePo.GetContents())
		runningSvcsLock.Lock()
		mfst, ok := runningServices[svcName]
		runningSvcsLock.Unlock()

		if ok {
			stopService(mfst)
		} else {
			olog <- SLM{Service: svcName, Message: "Service not found"}
		}
	}
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
				ServiceName: mfstPtr.ServiceName,
				Entity:      mfstPtr.Entity,
				Image:       mfstPtr.Image,
				MemAlloc:    mfstPtr.MemAlloc,
				CPUShares:   mfstPtr.CPUShares,
				Build:       mfstPtr.Build,
				Run:         mfstPtr.Run,
				AutoRestart: mfstPtr.AutoRestart,
				RestartInt:  mfstPtr.RestartInt,
				Volumes:     mfstPtr.Volumes,
			}
		}
		runningSvcsLock.Unlock()

		mfstPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumMsgPack, manifests)
		if err != nil {
			fmt.Println("Failed to marshal manifests for persistence")
		} else {
			spInterface.PublishSignal("manifests", mfstPo)
		}

		time.Sleep(persistManifestPeriod * time.Second)
	}
}

func recoverPreviousState() {
	mfstMsg := bwClient.QueryOneOrExit(&bw2.QueryParams{
		URI: spInterface.SignalURI("manifests"),
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
