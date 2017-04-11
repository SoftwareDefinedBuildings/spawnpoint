package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
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

const versionNum = `0.5.2`
const defaultZombiePeriod = 2 * time.Minute
const persistEnvVar = "SPAWND_PERSIST_DIR"
const logReaderBufSize = 1024
const logDuration = 2

var bwClients []*bw2.BW2Client
var cfgs []DaemonConfig
var ologs [](chan SLM)

var spInterfaces [](*bw2.Interface)

var (
	totalMem           []uint64 // Memory dedicated to Spawnpoint, in MiB
	totalCPUShares     []uint64 // CPU shares for Spawnpoint, 1024 per core
	availableMem       []int64
	availableCPUShares []int64
	availLocks         []sync.Mutex
)

var (
	runningServices  [](map[string]*Manifest)
	runningSvcsLocks []sync.Mutex
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
	app.Version = versionNum

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
		{
			Name:   "decommission",
			Usage:  "Decomission a spawnpoint daemon",
			Action: actionDecommission,
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

func readConfigFromFile(fileName string) ([]DaemonConfig, error) {
	configContents, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	chunks := strings.Split(string(configContents), "\n\n")
	configs := make([]DaemonConfig, len(chunks))
	for i, chunk := range chunks {
		if err := yaml.Unmarshal([]byte(chunk), &(configs[i])); err != nil {
			return nil, err
		}
	}
	return configs, nil
}

func readMetadataFromFile(fileName string) ([]map[string]string, error) {
	mdContents, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}

	chunks := strings.Split(string(mdContents), "\n\n")
	metadata := make([]map[string]string, len(chunks))
	for i, chunk := range chunks {
		if err = yaml.Unmarshal([]byte(chunk), &(metadata[i])); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func initializeBosswave() ([]*bw2.BW2Client, error) {
	clients := make([]*bw2.BW2Client, len(cfgs))
	var err error
	for i, cfg := range cfgs {
		bw2.SilenceLog()
		clients[i], err = bw2.Connect(cfg.LocalRouter)
		if err != nil {
			return nil, err
		}

		_, err = clients[i].SetEntityFile(cfg.Entity)
		if err != nil {
			return nil, err
		}

		clients[i].OverrideAutoChainTo(true)
	}
	return clients, nil
}

func actionDecommission(c *cli.Context) error {
	// Don't want to shadow global `cfgs` variable (fixme?)
	var err error
	cfgs, err = readConfigFromFile(c.String("config"))
	if err != nil {
		fmt.Println("Config file error:", err)
		os.Exit(1)
	}

	bwClients, err := initializeBosswave()
	if err != nil {
		fmt.Println("Failed to connect to Bosswave:", err)
		os.Exit(1)
	}

	for i, cfg := range cfgs {
		service := bwClients[i].RegisterService(cfg.Path, "s.spawnpoint")
		iface := service.RegisterInterface("server", "i.spawnpoint")
		// Publishing a message without any POs is effectively a "de-persist"
		err = iface.PublishSignal("heartbeat")
		if err != nil {
			fmt.Printf("Failed to decommission spawnpoint %s: %v\n", cfg.Alias, err)
		}
	}
	return nil
}

func actionRun(c *cli.Context) error {
	runningServices = make([](map[string]*Manifest), len(cfgs))

	var err error
	cfgs, err = readConfigFromFile(c.String("config"))
	if err != nil {
		fmt.Println("Config file error", err)
		os.Exit(1)
	}

	totalCPUShares = make([]uint64, len(cfgs))
	availableCPUShares = make([]int64, len(cfgs))
	totalMem = make([]uint64, len(cfgs))
	availableMem = make([]int64, len(cfgs))
	availLocks = make([]sync.Mutex, len(cfgs))

	for i, cfg := range cfgs {
		totalCPUShares[i] = cfg.CPUShares
		availableCPUShares[i] = int64(cfg.CPUShares)
		totalMem[i], err = objects.ParseMemAlloc(cfg.MemAlloc)
		if err != nil {
			fmt.Println("Invalid Spawnpoint memory allocation: " + cfg.MemAlloc)
			os.Exit(1)
		}
		availableMem[i] = int64(totalMem[i])
	}

	bwClients, err = initializeBosswave()
	if err != nil {
		fmt.Println("Failed to initialize Bosswave router:", err)
		os.Exit(1)
	}

	// Register spawnpoint service and interfaces
	spServices := make([]*bw2.Service, len(cfgs))
	spInterfaces = make([]*bw2.Interface, len(cfgs))
	for i, cfg := range cfgs {
		spServices[i] = bwClients[i].RegisterService(cfg.Path, "s.spawnpoint")
		spInterfaces[i] = spServices[i].RegisterInterface("server", "i.spawnpoint")

		spInterfaces[i].SubscribeSlot("config", curryHandleConfig(i))
		spInterfaces[i].SubscribeSlot("restart", curryHandleRestart(i))
		spInterfaces[i].SubscribeSlot("stop", curryHandleStop(i))
		spInterfaces[i].SubscribeSlot("logs", curryHandleLogs(i))
	}

	// Set Spawnpoint metadata
	if c.String("metadata") != "" {
		var metadata [](map[string]string)
		metadata, err = readMetadataFromFile(c.String("metadata"))
		if err != nil {
			fmt.Println("Invalid metadata file:", err)
			os.Exit(1)
		}
		for i, spService := range spServices {
			md := metadata[i]
			service := spService
			go func() {
				for {
					for mdKey, mdVal := range md {
						service.SetMetadata(mdKey, mdVal)
					}
					time.Sleep(heartbeatPeriod * time.Second)
				}
			}()
		}
	}

	// Start docker connection
	dockerEventCh, err := ConnectDocker()
	if err != nil {
		fmt.Println("Failed to connect to Docker:", err)
		os.Exit(1)
	}
	go monitorDockerEvents(&dockerEventCh)

	// If possible, recover state from persistence file
	runningServices = make([](map[string]*Manifest), len(cfgs))
	runningSvcsLocks = make([]sync.Mutex, len(cfgs))
	for i := 0; i < len(runningServices); i++ {
		runningServices[i] = make(map[string]*Manifest)
	}

	ologs = make([](chan SLM), len(cfgs))
	for i := 0; i < len(ologs); i++ {
		ologs[i] = make(chan SLM, 10)
	}
	recoverPreviousState()

	fmt.Printf("Spawnpoint Daemon - Version %s%s%s\n", ansi.ColorCode("cyan+b"),
		versionNum, ansi.ColorCode("reset"))
	for i := 0; i < len(cfgs); i++ {
		go heartbeat(i)
		go persistManifests(i)
		go publishMessages(i)
	}

	for {
		time.Sleep(10 * time.Second)
	}
}

func publishMessages(id int) error {
	// Publish outgoing log messages
	alias := (cfgs)[id].Alias
	for msg := range ologs[id] {
		fmt.Printf("%s%s (%s)::%s %s\n", ansi.ColorCode("blue+b"), msg.Service, alias,
			ansi.ColorCode("reset"), msg.Message)
		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, objects.SPLogMsg{
			Time:     time.Now().UnixNano(),
			SPAlias:  alias,
			Service:  msg.Service,
			Contents: msg.Message,
		})
		if err != nil {
			msg := fmt.Sprintf("(%s) Failed to construct log message: %v", alias, err)
			fmt.Println(msg)
			return errors.New(msg)
		}

		if err := spInterfaces[id].PublishSignal("log", po); err != nil {
			fmt.Printf("%s[WARN]%s Failed to publish log message: %v\n", ansi.ColorCode("yellow+b"),
				ansi.ColorCode("reset"), err)
		}
	}

	// Not reached
	return nil
}

func heartbeat(id int) {
	alias := cfgs[id].Alias
	for {
		availLocks[id].Lock()
		mem := availableMem[id]
		shares := availableCPUShares[id]
		availLocks[id].Unlock()

		fmt.Printf("%s(%s)%s Memory (MiB): %v, CPU Shares: %v\n", ansi.ColorCode("blue+b"),
			alias, ansi.ColorCode("reset"), mem, shares)
		msg := objects.SpawnPointHb{
			Alias:              alias,
			Time:               time.Now().UnixNano(),
			TotalMem:           totalMem[id],
			TotalCPUShares:     totalCPUShares[id],
			AvailableMem:       mem,
			AvailableCPUShares: shares,
		}
		hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointHeartbeat, msg)
		if err != nil {
			panic(err)
		}

		if err = spInterfaces[id].PublishSignal("heartbeat", hbPo); err != nil {
			fmt.Printf("%s[WARN]%s Failed to publish log message: %v\n", ansi.ColorCode("yellow+b"),
				ansi.ColorCode("reset"), err)
		}
		time.Sleep(heartbeatPeriod * time.Second)
	}
}

func monitorDockerEvents(ec *chan *docker.APIEvents) {
	for event := range *ec {
		if event.Action == "die" {
		outer:
			for i := 0; i < len(cfgs); i++ {
				runningSvcsLocks[i].Lock()
				for _, manifest := range runningServices[i] {
					if manifest.Container != nil && manifest.Container.Raw.ID == event.Actor.ID {
						*manifest.eventChan <- die
						runningSvcsLocks[i].Unlock()
						break outer
					}
				}
				runningSvcsLocks[i].Unlock()
			}
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

func handleConfig(id int, msg *bw2.SimpleMessage) {
	var trueCfg *objects.SvcConfig

	defer func() {
		r := recover()
		if r != nil {
			var tag string
			if trueCfg != nil {
				tag = trueCfg.ServiceName
			} else {
				tag = "meta"
			}
			ologs[id] <- SLM{Service: tag, Message: fmt.Sprintf("[FAILURE] Unable to launch service: %+v", r)}
		}
	}()

	// We can assume that POs have same ordering at publisher and subscriber
	trueCfgPo, ok := msg.POs[0].(bw2.YAMLPayloadObject)
	if !ok {
		panic("Service deployment config is not YAML")
	} else if !msg.POs[0].IsTypeDF(bw2.PODFSpawnpointConfig) {
		panic("Service deployment config has invalid PO type")
	}
	trueCfg = &objects.SvcConfig{}
	if err := trueCfgPo.ValueInto(&trueCfg); err != nil {
		panic(err)
	}
	if !msg.POs[1].IsTypeDF(bw2.PODFString) {
		panic("Malformed service config in deployment message")
	}
	origCfg := string(msg.POs[1].GetContents())

	if trueCfg.Image == "" {
		trueCfg.Image = defaultSpawnpointImage
	}
	rawMem := trueCfg.MemAlloc
	memAlloc, err := objects.ParseMemAlloc(rawMem)
	if err != nil {
		panic(err)
	}

	econtents, err := base64.StdEncoding.DecodeString(trueCfg.Entity)
	if err != nil {
		panic(err)
	}

	var fileIncludeEnc string
	if len(msg.POs) > 2 {
		if !msg.POs[2].IsTypeDF(bw2.PODFString) {
			panic("Malformed included file encoding in deployment message")
		}
		fileIncludeEnc = string(msg.POs[2].GetContents())
	}
	buildcontents, err := constructBuildContents(trueCfg, fileIncludeEnc)
	if err != nil {
		panic(err)
	}
	var restartWaitDur time.Duration
	if trueCfg.RestartInt != "" {
		restartWaitDur, err = time.ParseDuration(trueCfg.RestartInt)
		if err != nil {
			panic(err)
		}
	}

	var zombiePeriod time.Duration
	if trueCfg.ZombiePeriod != "" {
		zombiePeriod, err = time.ParseDuration(trueCfg.ZombiePeriod)
		if err != nil {
			panic(err)
		}
	} else {
		zombiePeriod = defaultZombiePeriod
	}

	if trueCfg.UseHostNet && !cfgs[id].AllowHostNet {
		alias := cfgs[id].Alias
		err := fmt.Errorf("Spawnpoint %s does not allow use of host network stack", alias)
		panic(err)
	}

	evCh := make(chan svcEvent, 5)
	logger := NewLogger(bwClients[id], cfgs[id].Path, cfgs[id].Alias, trueCfg.ServiceName)
	mf := Manifest{
		ServiceName:    trueCfg.ServiceName,
		Entity:         econtents,
		Image:          trueCfg.Image,
		MemAlloc:       memAlloc,
		CPUShares:      trueCfg.CPUShares,
		Build:          buildcontents,
		Run:            trueCfg.Run,
		AutoRestart:    trueCfg.AutoRestart,
		RestartInt:     restartWaitDur,
		Volumes:        trueCfg.Volumes,
		logger:         logger,
		OverlayNet:     trueCfg.OverlayNet,
		UseHostNet:     trueCfg.UseHostNet,
		eventChan:      &evCh,
		OriginalConfig: origCfg,
		ZombiePeriod:   zombiePeriod,
	}
	go manageService(id, &mf)
	evCh <- boot
}

func curryHandleConfig(id int) func(*bw2.SimpleMessage) {
	return func(msg *bw2.SimpleMessage) {
		handleConfig(id, msg)
	}
}

func handleRestart(id int, msg *bw2.SimpleMessage) {
	namePo := msg.GetOnePODF(bw2.PODFString)
	if namePo != nil {
		svcName := string(namePo.GetContents())
		runningSvcsLocks[id].Lock()
		mfst, ok := runningServices[id][svcName]
		runningSvcsLocks[id].Unlock()

		if ok && mfst.Container != nil {
			// Restart a running service
			*mfst.eventChan <- restart
		} else if ok {
			// Restart a zombie service
			*mfst.eventChan <- resurrect
		} else {
			ologs[id] <- SLM{Service: svcName, Message: "[FAILURE] Service not found"}
		}
	}
}

func curryHandleRestart(id int) func(*bw2.SimpleMessage) {
	return func(msg *bw2.SimpleMessage) {
		handleRestart(id, msg)
	}
}

func handleStop(id int, msg *bw2.SimpleMessage) {
	namePo := msg.GetOnePODF(bw2.PODFString)
	if namePo != nil {
		svcName := string(namePo.GetContents())
		runningSvcsLocks[id].Lock()
		mfst, ok := runningServices[id][svcName]
		runningSvcsLocks[id].Unlock()

		if ok {
			*mfst.eventChan <- stop
		} else {
			ologs[id] <- SLM{Service: svcName, Message: "[FAILURE] Service not found"}
		}
	}
}

func curryHandleStop(id int) func(*bw2.SimpleMessage) {
	return func(msg *bw2.SimpleMessage) {
		handleStop(id, msg)
	}
}

func handleLogs(id int, msg *bw2.SimpleMessage) {
	requestPo := msg.GetOnePODF(bw2.PODFMsgPack)
	if requestPo != nil {
		var request objects.LogsRequest
		if err := requestPo.(bw2.MsgPackPayloadObject).ValueInto(&request); err != nil {
			fmt.Println("Failed to unmarshal log request:", err)
		}

		runningSvcsLocks[id].Lock()
		_, ok := runningServices[id][request.SvcName]
		runningSvcsLocks[id].Unlock()
		if !ok {
			ologs[id] <- SLM{Service: request.SvcName, Message: "[FAILURE] Service not found"}
		}

		containerName := fmt.Sprintf("spawnpoint_%s_%s", cfgs[id].Alias, request.SvcName)
		reader, writer := io.Pipe()
		defer reader.Close()
		go GetLogs(writer, containerName, request.StartTime)
		// This is a hack because the Docker API's log call doesn't return
		// and doesn't respect contexts either
		time.AfterFunc(logDuration*time.Second, func() { writer.Close() })

		buffer := make([]string, logReaderBufSize)
		nextIdx := 0
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			buffer[nextIdx] = scanner.Text()
			nextIdx++

			if nextIdx == len(buffer) {
				logResponse := objects.LogsResponse{
					Nonce:    request.Nonce,
					Messages: buffer,
				}
				if err := sendLogResponse(&logResponse, spInterfaces[id], "logs/"+request.SvcName); err != nil {
					fmt.Println("Warning -- failed to publish logs response:", err)
				}
				nextIdx = 0
			}
		}
		if nextIdx > 0 {
			logResponse := objects.LogsResponse{
				Nonce:    request.Nonce,
				Messages: buffer[:nextIdx],
			}
			if err := sendLogResponse(&logResponse, spInterfaces[id], "logs/"+request.SvcName); err != nil {
				fmt.Println("Warning -- failed to publish logs response:", err)
			}
		}

		// Send empty logs response to signal end of messages to client
		logResponse := objects.LogsResponse{
			Nonce:    request.Nonce,
			Messages: []string{},
		}
		if err := sendLogResponse(&logResponse, spInterfaces[id], "logs/"+request.SvcName); err != nil {
			fmt.Println("Warning -- failed to publish final logs response:", err)
		}
	}
}

func curryHandleLogs(id int) func(*bw2.SimpleMessage) {
	return func(msg *bw2.SimpleMessage) {
		handleLogs(id, msg)
	}
}

func sendLogResponse(response *objects.LogsResponse, ifc *bw2.Interface, signal string) error {
	logRespPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumMsgPack, response)
	if err != nil {
		return fmt.Errorf("Failed to create log response PO: %v", err)
	}
	if err = ifc.PublishSignal(signal, logRespPo); err != nil {
		return fmt.Errorf("Failed to publish logs response: %v", err)
	}
	return nil
}

func manageService(id int, mfst *Manifest) {
	alias := cfgs[id].Alias
	for event := range *mfst.eventChan {
		switch event {
		case boot:
			// Previous version of service could already be running
			runningSvcsLocks[id].Lock()
			existingManifest, ok := runningServices[id][mfst.ServiceName]
			runningSvcsLocks[id].Unlock()
			previousMem := int64(0)
			previousCPU := int64(0)
			if ok && existingManifest.Container != nil {
				ologs[id] <- SLM{mfst.ServiceName, "[INFO] Found instance of service already running"}
				previousMem = int64(existingManifest.MemAlloc)
				previousCPU = int64(existingManifest.CPUShares)
			}

			// Check if Spawnpoint has sufficient resources. If not, reject configuration
			availLocks[id].Lock()
			if int64(mfst.MemAlloc) > (availableMem[id] + int64(previousMem)) {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
					availableMem[id]+previousMem, mfst.MemAlloc)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Unlock()
				return
			} else if int64(mfst.CPUShares) > (availableCPUShares[id] + int64(previousCPU)) {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
					availableCPUShares[id]+previousCPU, mfst.CPUShares)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Unlock()
				return
			} else {
				availableMem[id] -= int64(mfst.MemAlloc)
				availableCPUShares[id] -= int64(mfst.CPUShares)
				availLocks[id].Unlock()
			}

			// Remove previous version if necessary
			if ok && existingManifest.Container != nil {
				existingManifest.stopping = true
				err := StopContainer(alias, existingManifest.ServiceName, true)
				if err != nil {
					ologs[id] <- SLM{mfst.ServiceName, "[FAILURE] Unable to remove existing service"}
					existingManifest.stopping = false
					return
				}
			}

			// Add the new manifest before we start the container
			// We don't want any time interval where the container is running but doesn't have a manifest
			runningSvcsLocks[id].Lock()
			runningServices[id][mfst.ServiceName] = mfst
			runningSvcsLocks[id].Unlock()

			// Now start the container
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Booting service"}
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, true)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to (re)start container: %v", err)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Lock()
				availableMem[id] += int64(mfst.MemAlloc)
				availableCPUShares[id] += int64(mfst.CPUShares)
				availLocks[id].Unlock()

				runningSvcsLocks[id].Lock()
				delete(runningServices[id], mfst.ServiceName)
				runningSvcsLocks[id].Unlock()
				return
			}
			mfst.Container = container
			msg := "[SUCCESS] Container (re)start successful"
			ologs[id] <- SLM{mfst.ServiceName, msg}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case restart:
			// Restart a currently running service
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting restart"}
			mfst.restarting = true
			err := StopContainer(alias, mfst.ServiceName, false)
			if err != nil {
				mfst.restarting = false
				ologs[id] <- SLM{mfst.ServiceName, "[FAILURE] Unable to stop existing service"}
				continue
			}
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Stopped existing service"}

			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, false)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to restart container: %v", err)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				runningSvcsLocks[id].Lock()
				delete(runningServices[id], mfst.ServiceName)
				runningSvcsLocks[id].Unlock()

				availLocks[id].Lock()
				availableMem[id] += int64(mfst.MemAlloc)
				availableCPUShares[id] += int64(mfst.CPUShares)
				availLocks[id].Unlock()
				return
			}
			mfst.Container = container
			ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container restart successful"}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case autoRestart:
			// Auto-restart a service that has just terminated
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting auto-restart"}
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, false)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to auto-restart container: %v", err)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				runningSvcsLocks[id].Lock()
				delete(runningServices[id], mfst.ServiceName)
				runningSvcsLocks[id].Unlock()

				availLocks[id].Lock()
				availableMem[id] += int64(mfst.MemAlloc)
				availableCPUShares[id] += int64(mfst.CPUShares)
				availLocks[id].Unlock()
				return
			}
			mfst.Container = container
			ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container auto-restart successful"}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case resurrect:
			// Try to start up a recently terminated service
			// Start by checking if we have sufficient resources
			availLocks[id].Lock()
			if int64(mfst.MemAlloc) > availableMem[id] {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint memory for requested allocation (have %d, want %d)",
					availableMem[id], mfst.MemAlloc)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Unlock()
				// We can just let the deferred manifest removal take effect
				return
			} else if int64(mfst.CPUShares) > availableCPUShares[id] {
				msg := fmt.Sprintf("[FAILURE] Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
					availableCPUShares, mfst.CPUShares)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Unlock()
				// We can just let the deferred manifest removal take effect
				return
			} else {
				availableMem[id] -= int64(mfst.MemAlloc)
				availableCPUShares[id] -= int64(mfst.CPUShares)
				availLocks[id].Unlock()
			}

			// Now restart the container
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting to restart container"}
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, false)
			if err != nil {
				msg := fmt.Sprintf("[FAILURE] Unable to (re)start container: %v", err)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Lock()
				availableMem[id] += int64(mfst.MemAlloc)
				availableCPUShares[id] += int64(mfst.CPUShares)
				availLocks[id].Unlock()
				return
			}
			mfst.Container = container
			msg := "[SUCCESS] Container (re)start successful"
			ologs[id] <- SLM{mfst.ServiceName, msg}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case stop:
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting to stop container"}
			if mfst.Container == nil {
				ologs[id] <- SLM{mfst.ServiceName, "[INFO] Container is already stopped"}
			} else {
				// Updating available mem and cpu shares done by event monitor
				err := StopContainer(alias, mfst.ServiceName, true)
				if err != nil {
					msg := fmt.Sprintf("[FAILURE] Unable to stop container: %v", err)
					ologs[id] <- SLM{mfst.ServiceName, msg}
				} else {
					mfst.stopping = true
					ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container stopped"}
				}
			}

		case die:
			if mfst.restarting {
				mfst.restarting = false
				continue
			}
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Container has stopped"}
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
				availLocks[id].Lock()
				availableCPUShares[id] += int64(mfst.CPUShares)
				availableMem[id] += int64(mfst.MemAlloc)
				availLocks[id].Unlock()

				// Defer removal of the manifest in case user wants to restart
				time.AfterFunc(mfst.ZombiePeriod, func() {
					runningSvcsLocks[id].Lock()
					latestMfst, ok := runningServices[id][mfst.ServiceName]
					if ok && latestMfst.Container == nil {
						delete(runningServices[id], mfst.ServiceName)
						close(*mfst.eventChan)
					}
					runningSvcsLocks[id].Unlock()

					depersistSvcHb(id, mfst.ServiceName)
				})
			}

		case adopt:
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Reassuming ownership upon spawnd restart"}
			availLocks[id].Lock()
			availableMem[id] -= int64(mfst.MemAlloc)
			availableCPUShares[id] -= int64(mfst.CPUShares)
			availLocks[id].Unlock()
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)
		}
	}
}

func svcHeartbeat(id int, svcname string, statCh *chan *docker.Stats) {
	spURI := cfgs[id].Path

	lastEmitted := time.Now()
	lastCPUPercentage := 0.0
	for stats := range *statCh {
		if stats.Read.Sub(lastEmitted).Seconds() > heartbeatPeriod {
			runningSvcsLocks[id].Lock()
			manifest, ok := runningServices[id][svcname]
			runningSvcsLocks[id].Unlock()
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
				SpawnpointURI:  spURI,
				Name:           svcname,
				Time:           time.Now().UnixNano(),
				MemAlloc:       manifest.MemAlloc,
				CPUShares:      manifest.CPUShares,
				MemUsage:       float64(stats.MemoryStats.Usage) / (1024 * 1024),
				NetworkRx:      float64(stats.Network.RxBytes) / (1024 * 1024),
				NetworkTx:      float64(stats.Network.TxBytes) / (1024 * 1024),
				MbRead:         mbRead,
				MbWritten:      mbWritten,
				CPUPercent:     lastCPUPercentage,
				OriginalConfig: manifest.OriginalConfig,
			}

			hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointSvcHb, msg)
			if err != nil {
				panic(err)
			}
			if err = spInterfaces[id].PublishSignal("heartbeat/"+svcname, hbPo); err != nil {
				fmt.Printf("%s[WARN]%s Failed to publish heartbeat for service %s: %v\n",
					ansi.ColorCode("yellow+b"), ansi.ColorCode("reset"), svcname, err)
			}

			lastEmitted = time.Now()
		}
	}
}

func depersistSvcHb(id int, svcname string) {
	// Publishing a message with no POs is effectively a "de-persist" operation
	if err := spInterfaces[id].PublishSignal("heartbeat/" + svcname); err != nil {
		fmt.Printf("%s[WARN]%s Failed to de-persist heartbeat for servfice %s: %v\n",
			ansi.ColorCode("yellow+b"), ansi.ColorCode("reset"), svcname, err)
	}
}

func persistManifests(id int) {
	for {
		manifests := make([]Manifest, len(runningServices[id]))
		i := 0

		runningSvcsLocks[id].Lock()
		for _, mfstPtr := range runningServices[id] {
			// Make a deep copy
			manifests[i] = Manifest{
				ServiceName:    mfstPtr.ServiceName,
				Entity:         mfstPtr.Entity,
				Image:          mfstPtr.Image,
				MemAlloc:       mfstPtr.MemAlloc,
				CPUShares:      mfstPtr.CPUShares,
				Build:          mfstPtr.Build,
				Run:            mfstPtr.Run,
				AutoRestart:    mfstPtr.AutoRestart,
				RestartInt:     mfstPtr.RestartInt,
				Volumes:        mfstPtr.Volumes,
				OverlayNet:     mfstPtr.OverlayNet,
				UseHostNet:     mfstPtr.UseHostNet,
				OriginalConfig: mfstPtr.OriginalConfig,
				ZombiePeriod:   mfstPtr.ZombiePeriod,
			}
			i++
		}
		runningSvcsLocks[id].Unlock()

		var manifestRaw bytes.Buffer
		mfstFileDest := ".manifests-" + cfgs[id].Alias
		if mfstFileEnv := os.Getenv(persistEnvVar); mfstFileEnv != "" {
			mfstFileDest = mfstFileEnv + "/" + mfstFileDest
		}

		encoder := gob.NewEncoder(&manifestRaw)
		if err := encoder.Encode(manifests); err != nil {
			fmt.Printf("%s[WARN]%s Failed to encode manifests: %v\n", ansi.ColorCode("yellow+b"),
				ansi.ColorCode("reset"), err)
		} else if err := ioutil.WriteFile(mfstFileDest, manifestRaw.Bytes(), 0600); err != nil {
			fmt.Printf("%s[WARN]%s Failed to encode manifests: %v\n", ansi.ColorCode("yellow+b"),
				ansi.ColorCode("reset"), err)
		}
		time.Sleep(persistManifestPeriod * time.Second)
	}
}

func recoverPreviousState() {
	for i := 0; i < len(cfgs); i++ {
		mfstFileSrc := ".manifests-" + cfgs[i].Alias
		if mfstFileEnv := os.Getenv(persistEnvVar); mfstFileEnv != "" {
			mfstFileSrc = mfstFileEnv + "/" + mfstFileSrc
		}
		mfstBytes, err := ioutil.ReadFile(mfstFileSrc)
		if err != nil {
			fmt.Printf("Error (%s): Failed to read persisted manifests: %v", cfgs[i].Alias, err)
			return
		}

		var priorManifests []Manifest
		decoder := gob.NewDecoder(bytes.NewReader(mfstBytes))
		if err = decoder.Decode(&priorManifests); err != nil {
			fmt.Printf("Error (%s): Failed to decode persisted manifests: %v", cfgs[i].Alias, err)
		}

		knownContainers, err := GetSpawnedContainers(cfgs[i].Alias)
		if err != nil {
			fmt.Printf("Failed to retrieve container info: %v\n", err)
			return
		}

		reclaimedManifests := make(map[string]*Manifest)
		for _, mfst := range priorManifests {
			thisMfst := mfst
			newEvChan := make(chan svcEvent, 5)
			thisMfst.eventChan = &newEvChan
			logger := NewLogger(bwClients[i], cfgs[i].Path, cfgs[i].Alias, thisMfst.ServiceName)
			thisMfst.logger = logger
			containerInfo, ok := knownContainers[thisMfst.ServiceName]
			if ok && containerInfo.Raw.State.Running {
				thisMfst.Container = ReclaimContainer(&thisMfst, containerInfo.Raw)
				go manageService(i, &thisMfst)
				reclaimedManifests[thisMfst.ServiceName] = &thisMfst
				newEvChan <- adopt
			} else if mfst.AutoRestart {
				go manageService(i, &thisMfst)
				newEvChan <- boot
			}
		}
		runningSvcsLocks[i].Lock()
		for name, mfst := range reclaimedManifests {
			runningServices[i][name] = mfst
		}
		runningSvcsLocks[i].Unlock()
	}
}
