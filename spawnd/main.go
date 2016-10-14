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

const heartbeatPeriod = 5
const persistManifestPeriod = 10
const defaultSpawnpointImage = "jhkolb/spawnpoint:amd64"

type SLM struct {
	Service string
	Message string
}

type svcEvent int

const (
	boot        svcEvent = iota
	restart     svcEvent = iota
	stop        svcEvent = iota
	die         svcEvent = iota
	adopt       svcEvent = iota
	resurrect   svcEvent = iota
	autoRestart svcEvent = iota
)

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
	bw2.SilenceLog()
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
		var metadata *map[string]string
		metadata, err = readMetadataFromFile(c.String("metadata"))
		if err != nil {
			fmt.Println("Invalid metadata file:", err)
			os.Exit(1)
		}
		go func() {
			for {
				for mdKey, mdVal := range *metadata {
					spService.SetMetadata(mdKey, mdVal)
				}
				time.Sleep(heartbeatPeriod * time.Second)
			}
		}()
	}

	// Start docker connection
	dockerEventCh, err := ConnectDocker()
	if err != nil {
		fmt.Println("Failed to connect to Docker:", err)
		os.Exit(1)
	}
	go monitorDockerEvents(&dockerEventCh)

	// If possible, recover state from persistence file
	runningServices = make(map[string]*Manifest)
	recoverPreviousState()

	fmt.Printf("Spawnpoint Daemon - Version %s%s%s\n", ansi.ColorCode("cyan+b"),
		objects.SpawnpointVersion, ansi.ColorCode("reset"))
	go heartbeat()
	go persistManifests()

	// Publish outgoing log messages
	for msg := range olog {
		fmt.Printf("%s%s ::%s %s\n", ansi.ColorCode("blue+b"), msg.Service, ansi.ColorCode("reset"),
			msg.Message)
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
			fmt.Printf("%s[WARN]%s Failed to publish log message: %v\n", ansi.ColorCode("yellow+b"),
				ansi.ColorCode("reset"), err)
		}
	}

	// Not reached
	return nil
}

func heartbeat() {
	for {
		availLock.Lock()
		mem := availableMem
		shares := availableCPUShares
		availLock.Unlock()

		fmt.Printf("Memory (MiB): %v, CPU Shares: %v\n", mem, shares)
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

		if err = spInterface.PublishSignal("heartbeat", hbPo); err != nil {
			fmt.Printf("%s[WARN]%s Failed to publish log message: %v\n", ansi.ColorCode("yellow+b"),
				ansi.ColorCode("reset"), err)
		}
		time.Sleep(heartbeatPeriod * time.Second)
	}
}

func monitorDockerEvents(ec *chan *docker.APIEvents) {
	for event := range *ec {
		if event.Action == "die" {
			runningSvcsLock.Lock()
			for _, manifest := range runningServices {
				if manifest.Container != nil && manifest.Container.Raw.ID == event.Actor.ID {
					*manifest.eventChan <- die
					break
				}
			}
			runningSvcsLock.Unlock()
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
			olog <- SLM{Service: tag, Message: fmt.Sprintf("[FAILURE] Unable to launch service: %+v", r)}
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

	evCh := make(chan svcEvent, 5)
	logger := NewLogger(bwClient, cfg.Path, cfg.Alias, config.ServiceName)
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
		logger:      logger,
		OverlayNet:  config.OverlayNet,
		eventChan:   &evCh,
	}
	go manageService(&mf)
	evCh <- boot
}

func handleRestart(msg *bw2.SimpleMessage) {
	namePo := msg.GetOnePODF(bw2.PODFString)
	if namePo != nil {
		svcName := string(namePo.GetContents())
		runningSvcsLock.Lock()
		mfst, ok := runningServices[svcName]
		runningSvcsLock.Unlock()

		if ok && mfst.Container != nil {
			// Restart a running service
			*mfst.eventChan <- restart
		} else if ok {
			// Restart a zombie service
			*mfst.eventChan <- resurrect
		} else {
			olog <- SLM{Service: svcName, Message: "[FAILURE] Service not found"}
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
			*mfst.eventChan <- stop
		} else {
			olog <- SLM{Service: svcName, Message: "[FAILURE] Service not found"}
		}
	}
}

func manageService(mfst *Manifest) {
	for event := range *mfst.eventChan {
		switch event {
		case boot:
			// Previous version of service could already be running
			runningSvcsLock.Lock()
			existingManifest, ok := runningServices[mfst.ServiceName]
			runningSvcsLock.Unlock()
			previousMem := int64(0)
			previousCPU := int64(0)
			if ok && existingManifest.Container != nil {
				olog <- SLM{mfst.ServiceName, "[INFO] Found instance of service already running"}
				previousMem = int64(existingManifest.MemAlloc)
				previousCPU = int64(existingManifest.CPUShares)
			}

			// Check if Spawnpoint has sufficient resources. If not, reject configuration
			availLock.Lock()
			if int64(mfst.MemAlloc) > (availableMem + int64(previousMem)) {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
					availableMem+previousMem, mfst.MemAlloc)
				olog <- SLM{mfst.ServiceName, msg}
				availLock.Unlock()
				return
			} else if int64(mfst.CPUShares) > (availableCPUShares + int64(previousCPU)) {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
					availableCPUShares+previousCPU, mfst.CPUShares)
				olog <- SLM{mfst.ServiceName, msg}
				availLock.Unlock()
				return
			} else {
				availableMem -= int64(mfst.MemAlloc)
				availableCPUShares -= int64(mfst.CPUShares)
				availLock.Unlock()
			}

			// Remove previous version if necessary
			if ok && existingManifest.Container != nil {
				mfst.restarting = true
				err := StopContainer(existingManifest.ServiceName, true)
				if err != nil {
					olog <- SLM{mfst.ServiceName, "[FAILURE] Unable to remove existing service"}
				} else {
					runningSvcsLock.Lock()
					delete(runningServices, existingManifest.ServiceName)
					runningSvcsLock.Unlock()
				}
			}

			// Now start the container
			olog <- SLM{mfst.ServiceName, "[INFO] Booting service"}
			container, err := RestartContainer(mfst, cfg.ContainerRouter, true)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to (re)start container: %v", err)
				olog <- SLM{mfst.ServiceName, msg}
				availLock.Lock()
				availableMem += int64(mfst.MemAlloc)
				availableCPUShares += int64(mfst.CPUShares)
				availLock.Unlock()
				return
			}
			mfst.Container = container
			msg := "[SUCCESS] Container (re)start successful"
			olog <- SLM{mfst.ServiceName, msg}
			runningSvcsLock.Lock()
			runningServices[mfst.ServiceName] = mfst
			runningSvcsLock.Unlock()
			go svcHeartbeat(mfst.ServiceName, mfst.Container.StatChan)

		case restart:
			// Restart a currently running service
			olog <- SLM{mfst.ServiceName, "[INFO] Attempting restart"}
			mfst.restarting = true
			err := StopContainer(mfst.ServiceName, false)
			if err != nil {
				mfst.restarting = false
				olog <- SLM{mfst.ServiceName, "[FAILURE] Unable to stop existing service"}
				continue
			}
			olog <- SLM{mfst.ServiceName, "[INFO] Stopped existing service"}

			container, err := RestartContainer(mfst, cfg.ContainerRouter, false)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to restart container: %v", err)
				olog <- SLM{mfst.ServiceName, msg}
				runningSvcsLock.Lock()
				delete(runningServices, mfst.ServiceName)
				runningSvcsLock.Unlock()

				availLock.Lock()
				availableMem += int64(mfst.MemAlloc)
				availableCPUShares += int64(mfst.CPUShares)
				availLock.Unlock()
				return
			}
			mfst.Container = container
			olog <- SLM{mfst.ServiceName, "[SUCCESS] Container restart successful"}
			go svcHeartbeat(mfst.ServiceName, mfst.Container.StatChan)

		case autoRestart:
			// Auto-restart a service that has just terminated
			olog <- SLM{mfst.ServiceName, "[INFO] Attempting auto-restart"}
			container, err := RestartContainer(mfst, cfg.ContainerRouter, false)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to auto-restart container: %v", err)
				olog <- SLM{mfst.ServiceName, msg}
				runningSvcsLock.Lock()
				delete(runningServices, mfst.ServiceName)
				runningSvcsLock.Unlock()

				availLock.Lock()
				availableMem += int64(mfst.MemAlloc)
				availableCPUShares += int64(mfst.CPUShares)
				availLock.Unlock()
				return
			}
			mfst.Container = container
			olog <- SLM{mfst.ServiceName, "[SUCCESS] Container auto-restart successful"}
			go svcHeartbeat(mfst.ServiceName, mfst.Container.StatChan)

		case resurrect:
			// Try to start up a recently terminated service
			// Start by checking if we have sufficient resources
			availLock.Lock()
			if int64(mfst.MemAlloc) > availableMem {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
					availableMem, mfst.MemAlloc)
				olog <- SLM{mfst.ServiceName, msg}
				availLock.Unlock()
				// We can just let the deferred manifest removal take effect
				return
			} else if int64(mfst.CPUShares) > availableCPUShares {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
					availableCPUShares, mfst.CPUShares)
				olog <- SLM{mfst.ServiceName, msg}
				availLock.Unlock()
				// We can just let the deferred manifest removal take effect
				return
			} else {
				availableMem -= int64(mfst.MemAlloc)
				availableCPUShares -= int64(mfst.CPUShares)
				availLock.Unlock()
			}

			// Now restart the container
			olog <- SLM{mfst.ServiceName, "[INFO] Attempting to restart container"}
			container, err := RestartContainer(mfst, cfg.ContainerRouter, false)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to (re)start container: %v", err)
				olog <- SLM{mfst.ServiceName, msg}
				availLock.Lock()
				availableMem += int64(mfst.MemAlloc)
				availableCPUShares += int64(mfst.CPUShares)
				availLock.Unlock()
				return
			}
			mfst.Container = container
			msg := "[SUCCESS] Container (re)start successful"
			olog <- SLM{mfst.ServiceName, msg}
			go svcHeartbeat(mfst.ServiceName, mfst.Container.StatChan)

		case stop:
			olog <- SLM{mfst.ServiceName, "[INFO] Attempting to stop container"}
			if mfst.Container == nil {
				olog <- SLM{mfst.ServiceName, "[INFO] Container is already stopped"}
			} else {
				// Updating available mem and cpu shares done by event monitor
				err := StopContainer(mfst.ServiceName, true)
				if err != nil {
					msg := fmt.Sprintf("[FAILURE] Unable to stop container: %v", err)
					olog <- SLM{mfst.ServiceName, msg}
				} else {
					mfst.stopping = true
					olog <- SLM{mfst.ServiceName, "[SUCCESS] Container stopped"}
				}
			}

		case die:
			if mfst.restarting {
				mfst.restarting = false
				continue
			}
			olog <- SLM{mfst.ServiceName, "[INFO] Container has stopped"}
			if mfst.AutoRestart && !mfst.stopping {
				go func() {
					if mfst.RestartInt > 0 {
						time.Sleep(mfst.RestartInt)
					}
					*mfst.eventChan <- autoRestart
				}()
			} else {
				if mfst.stopping {
					mfst.stopping = false
				}
				mfst.Container = nil
				availLock.Lock()
				availableCPUShares += int64(mfst.CPUShares)
				availableMem += int64(mfst.MemAlloc)
				availLock.Unlock()

				// Defer removal of the manifest in case user wants to restart
				time.AfterFunc(objects.ZombiePeriod, func() {
					runningSvcsLock.Lock()
					latestMfst, ok := runningServices[mfst.ServiceName]
					if ok && latestMfst.Container == nil {
						delete(runningServices, mfst.ServiceName)
						close(*mfst.eventChan)
					}
					runningSvcsLock.Unlock()
				})
			}

		case adopt:
			olog <- SLM{mfst.ServiceName, "[INFO] Reassuming ownership upon spawnd restart"}
			availLock.Lock()
			availableMem -= int64(mfst.MemAlloc)
			availableCPUShares -= int64(mfst.CPUShares)
			availLock.Unlock()
		}
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
			if err = spInterface.PublishSignal("heartbeat/"+svcname, hbPo); err != nil {
				fmt.Printf("%s[WARN]%s Failed to publish heartbeat for service %s: %v",
					ansi.ColorCode("yellow+b"), ansi.ColorCode("reset"), svcname, err)
			}

			lastEmitted = time.Now()
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
			panic(err)
		} else {
			if err = spInterface.PublishSignal("manifests", mfstPo); err != nil {
				fmt.Printf("%s[WARN]%s Failed to persist manifests: %v\n", ansi.ColorCode("yellow+b"),
					ansi.ColorCode("reset"), err)
			}
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

	newManifests := make(map[string]*Manifest)
	for _, mfst := range priorManifests {
		newEvChan := make(chan svcEvent, 5)
		mfst.eventChan = &newEvChan
		logger := NewLogger(bwClient, cfg.Path, cfg.Alias, mfst.ServiceName)
		mfst.logger = logger
		containerInfo, ok := knownContainers[mfst.ServiceName]
		if ok && containerInfo.Raw.State.Running {
			go manageService(&mfst)
			mfst.Container = ReclaimContainer(&mfst, containerInfo.Raw)
		} else if mfst.AutoRestart {
			go manageService(&mfst)
			newEvChan <- boot
		}
	}
	runningSvcsLock.Lock()
	runningServices = newManifests
	runningSvcsLock.Unlock()
}
