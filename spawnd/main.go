package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/objects"
	"github.com/codegangsta/cli"
	"github.com/docker/docker/api/types/events"
	"github.com/mgutz/ansi"
	"github.com/op/go-logging"
	"github.com/pkg/errors"

	docker "github.com/fsouza/go-dockerclient"
	bw2 "github.com/immesys/bw2bind"
	yaml "gopkg.in/yaml.v2"
)

const versionNum = `0.5.7`
const defaultZombiePeriod = 2 * time.Minute
const persistEnvVar = "SPAWND_PERSIST_DIR"
const logReaderBufSize = 1024
const logDuration = 2

var (
	globalLog *logging.Logger
	logs      []*logging.Logger
)

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
			return nil, errors.Wrap(err, "Failed to parse config YAML")
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
			return nil, errors.Wrap(err, "Failed to connect to BW2 router")
		}

		_, err = clients[i].SetEntityFile(cfg.Entity)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to set BW2 entity")
		}

		clients[i].OverrideAutoChainTo(true)
	}
	return clients, nil
}

func initGlobalLogger() *logging.Logger {
	format := "%{color}%{level} %{time:Jan 02 15:04:05} %{shortfunc} %{color:reset}▶ %{message}"
	backend := logging.NewLogBackend(os.Stderr, "", 0)
	logBackendLeveled := logging.AddModuleLevel(backend)
	logging.SetBackend(logBackendLeveled)
	logging.SetFormatter(logging.MustStringFormatter(format))
	return logging.MustGetLogger("spawnd")
}

func initLoggers(cfgs *[]DaemonConfig) []*logging.Logger {
	logs = make([]*logging.Logger, len(*cfgs))
	for i, cfg := range *cfgs {
		format := "%{color}%{level} %{time:Jan 02 15:04:05} " + cfg.Alias +
			"(%{shortfunc}) %{color:reset}▶  %{message}"
		logging.SetFormatter(logging.MustStringFormatter(format))
		logs[i] = logging.MustGetLogger(("spawnd"))
	}
	return logs
}

func actionDecommission(c *cli.Context) error {
	globalLog = initGlobalLogger()

	// Don't want to shadow global `cfgs` variable (fixme?)
	var err error
	cfgs, err = readConfigFromFile(c.String("config"))
	if err != nil {
		globalLog.Fatalf("Failed to read config file: %s", err)
	}
	logs = initLoggers(&cfgs)

	bwClients, err = initializeBosswave()
	if err != nil {
		globalLog.Fatalf("Failed to connect to Bosswave: %s", err)
	}
	for i, cfg := range cfgs {
		service := bwClients[i].RegisterService(cfg.Path, "s.spawnpoint")
		iface := service.RegisterInterface("server", "i.spawnpoint")
		// Publishing a message without any POs is effectively a "de-persist"
		if err = iface.PublishSignal("heartbeat"); err != nil {
			logs[i].Fatalf("Failed to decommission spawnpoint %s: %v", cfg.Alias, err)
		}
	}
	return nil
}

func actionRun(c *cli.Context) error {
	globalLog = initGlobalLogger()
	runningServices = make([](map[string]*Manifest), len(cfgs))

	var err error
	cfgs, err = readConfigFromFile(c.String("config"))
	if err != nil {
		globalLog.Fatalf("Failed to read configuration file: %s", err)
	}
	logs = initLoggers(&cfgs)

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
			logs[i].Fatalf("Invalid memory allocation setting: %s", cfg.MemAlloc)
		}
		availableMem[i] = int64(totalMem[i])
	}

	bwClients, err = initializeBosswave()
	if err != nil {
		globalLog.Fatalf("Failed to initialize Bosswave router: %s", err)
	}

	// Register spawnpoint service and interfaces
	spServices := make([]*bw2.Service, len(cfgs))
	spInterfaces = make([]*bw2.Interface, len(cfgs))
	for i, cfg := range cfgs {
		spServices[i] = bwClients[i].RegisterService(cfg.Path, "s.spawnpoint")
		spServices[i].SetErrorHandler(func(err error) {
			logs[i].Errorf("Failed to register service metadata: %s", err)
		})
		spInterfaces[i] = spServices[i].RegisterInterface("server", "i.spawnpoint")

		if err = spInterfaces[i].SubscribeSlot("config", curryHandleConfig(i)); err != nil {
			logs[i].Fatalf("Failed to subscribe to slot %s: %s", spInterfaces[i].SlotURI("config"), err)
		} else {
			logs[i].Debugf("Subscribed to slot %s", spInterfaces[i].SlotURI("config"))
		}

		if err = spInterfaces[i].SubscribeSlot("restart", curryHandleRestart(i)); err != nil {
			logs[i].Fatalf("Failed to subscribe to slot %s: %s", spInterfaces[i].SlotURI("restart"), err)
		} else {
			logs[i].Debugf("Subscribed to slot %s", spInterfaces[i].SlotURI("restart"))
		}

		if err = spInterfaces[i].SubscribeSlot("stop", curryHandleStop(i)); err != nil {
			logs[i].Fatalf("Failed to subscribe to slot: %s: %s", spInterfaces[i].SlotURI("stop"), err)
		} else {
			logs[i].Debugf("Subscribed to slot %s", spInterfaces[i].SlotURI("stop"))
		}

		if err = spInterfaces[i].SubscribeSlot("logs", curryHandleLogs(i)); err != nil {
			logs[i].Fatalf("Failed to subscribe to slot: %s: %s", spInterfaces[i].SlotURI("logs"), err)
		} else {
			logs[i].Debugf("Subscribed to slot %s", spInterfaces[i].SlotURI("logs"))
		}
	}

	// Set Spawnpoint metadata
	if c.String("metadata") != "" {
		var metadata [](map[string]string)
		metadata, err = readMetadataFromFile(c.String("metadata"))
		if err != nil {
			globalLog.Fatalf("Invalid metadata file: %s", err)
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
	dockerEventCh, errCh, err := ConnectDocker()
	if err != nil {
		globalLog.Fatalf("Failed to connect to Docker daemon: %s", err)
	}
	go monitorDockerEvents(dockerEventCh, errCh)

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

	globalLog.Infof("Spawnpoint Daemon - Version %s%s%s\n", ansi.ColorCode("cyan+b"),
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
		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, objects.SPLogMsg{
			Time:     time.Now().UnixNano(),
			SPAlias:  alias,
			Service:  msg.Service,
			Contents: msg.Message,
		})
		if err != nil {
			logs[id].Errorf("Failed to serialize log message: %s", err)
			continue
		}

		if err := spInterfaces[id].PublishSignal("log", po); err != nil {
			logs[id].Errorf("Failed to publish log message: %s", err)
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

		logs[id].Infof("Memory (MiB): %v, CPU Shares: %v\n", mem, shares)
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
			logs[id].Errorf("Failed to publish heartbeat message: %s", err)
		}

		runningSvcsLocks[id].Lock()
		for _, manifest := range runningServices[id] {
			if manifest.Container != nil {
				logs[id].Debugf("Running service %s, Mem: %v MiB, CPU: %v Shares",
					manifest.ServiceName, manifest.MemAlloc, manifest.CPUShares)
			}
		}
		runningSvcsLocks[id].Unlock()
		time.Sleep(heartbeatPeriod * time.Second)
	}
}

func monitorDockerEvents(evCh *<-chan events.Message, errCh *<-chan error) {
	for {
		select {
		case event := <-*evCh:
			if event.Action == "die" {
				globalLog.Debugf("Docker container has died: %s", event.Actor.ID)
			outer:
				for i := 0; i < len(cfgs); i++ {
					runningSvcsLocks[i].Lock()
					for _, manifest := range runningServices[i] {
						if manifest.Container != nil && manifest.Container.Raw.ID == event.Actor.ID {
							logs[i].Debugf("Dead Docker container ID %s matches service %s, issuing die event",
								event.Actor.ID, manifest.ServiceName)
							*manifest.eventChan <- die
							runningSvcsLocks[i].Unlock()
							break outer
						}
					}
					runningSvcsLocks[i].Unlock()
				}
			}

		case err := <-*errCh:
			globalLog.Criticalf("Lost connection to Docker daemon: %s", err)
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
			err := fmt.Errorf("Unknown source type: %s", sourceparts[0])
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
	logs[id].Debug("Received new service configuration")
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

			logs[id].Errorf("failed to launch new service (%s) %s", tag, r)
			ologs[id] <- SLM{Service: tag, Message: fmt.Sprintf("[FAILURE] Unable to launch service: %v", r)}
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
		panic(errors.Wrap(err, "Failed to unmarshal config"))
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
		panic(errors.Wrap(err, "Failed to parse service memory allocation"))
	}

	econtents, err := base64.StdEncoding.DecodeString(trueCfg.Entity)
	if err != nil {
		panic(errors.Wrap(err, "Failed to decode entity base64 string"))
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
		panic(errors.Wrap(err, "Failed to generate build file contents"))
	}
	var restartWaitDur time.Duration
	if trueCfg.RestartInt != "" {
		restartWaitDur, err = time.ParseDuration(trueCfg.RestartInt)
		if err != nil {
			panic(errors.Wrap(err, "Failed to parse restart interval duration"))
		}
	}

	var zombiePeriod time.Duration
	if trueCfg.ZombiePeriod != "" {
		zombiePeriod, err = time.ParseDuration(trueCfg.ZombiePeriod)
		if err != nil {
			panic(errors.Wrap(err, "Failed to parse zombie interval duration"))
		}
	} else {
		zombiePeriod = defaultZombiePeriod
	}

	if trueCfg.UseHostNet && !cfgs[id].AllowHostNet {
		err := fmt.Errorf("Spawnpoint %s does not allow use of host network stack",
			cfgs[id].Alias)
		panic(err)
	}

	if len(trueCfg.Devices) > 0 && !cfgs[id].AllowDeviceMappings {
		err := fmt.Errorf("Spawnpoint %s does not allow host devices to be mapped into containers",
			cfgs[id].Alias)
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
		Devices:        trueCfg.Devices,
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
		logs[id].Debugf("Received restart command for service %s", svcName)
		runningSvcsLocks[id].Lock()
		mfst, ok := runningServices[id][svcName]
		var container *SpawnPointContainer
		if ok {
			container = mfst.Container
		}
		runningSvcsLocks[id].Unlock()

		if ok && container != nil {
			// Restart a running service
			logs[id].Debugf("Service %s already running, restarting", svcName)
			*mfst.eventChan <- restart
		} else if ok {
			// Restart a zombie service
			logs[id].Debugf("Service %s is in zombie state, restarting", svcName)
			*mfst.eventChan <- resurrect
		} else {
			logs[id].Debugf("Service %s not found, not restarting", svcName)
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
		logs[id].Debugf("Received command to stop service %s", svcName)
		runningSvcsLocks[id].Lock()
		mfst, ok := runningServices[id][svcName]
		runningSvcsLocks[id].Unlock()

		if ok {
			logs[id].Debugf("Service %s currently running, issuing stop event", svcName)
			*mfst.eventChan <- stop
		} else {
			logs[id].Debugf("Service %s not found, not stopping", svcName)
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
			logs[id].Error("Received log request, but failed to parse it")
			return
		}

		runningSvcsLocks[id].Lock()
		_, ok := runningServices[id][request.SvcName]
		runningSvcsLocks[id].Unlock()
		if !ok {
			logs[id].Debugf("Received log request for non-existent service %s", request.SvcName)
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
					logs[id].Errorf("Failed to publish response to logs request for service %s: %s", request.SvcName, err)
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
				logs[id].Errorf("Failed to publish response to logs request for service %s: %s", request.SvcName, err)
			}
		}

		// Send empty logs response to signal end of messages to client
		logResponse := objects.LogsResponse{
			Nonce:    request.Nonce,
			Messages: []string{},
		}
		if err := sendLogResponse(&logResponse, spInterfaces[id], "logs/"+request.SvcName); err != nil {
			logs[id].Errorf("Failed to publish response to logs request for service %s: %s", request.SvcName, err)
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
		return errors.Wrap(err, "Failed to create log response PO")
	}
	if err = ifc.PublishSignal(signal, logRespPo); err != nil {
		return errors.Wrap(err, "Bosswave publish failed")
	}
	return nil
}

func manageService(id int, mfst *Manifest) {
	alias := cfgs[id].Alias
	svcName := mfst.ServiceName
	log := logs[id]
	for event := range *mfst.eventChan {
		switch event {
		case boot:
			log.Debugf("(%s) State machine received boot event", svcName)
			// Previous version of service could already be running
			runningSvcsLocks[id].Lock()
			existingManifest, ok := runningServices[id][mfst.ServiceName]
			runningSvcsLocks[id].Unlock()
			previousMem := int64(0)
			previousCPU := int64(0)
			if ok && existingManifest.Container != nil {
				log.Debugf("(%s) Previous manifest already present for this service", svcName)
				ologs[id] <- SLM{mfst.ServiceName, "Found instance of service already running"}
				previousMem = int64(existingManifest.MemAlloc)
				previousCPU = int64(existingManifest.CPUShares)
			}

			// Check if Spawnpoint has sufficient resources. If not, reject configuration
			availLocks[id].Lock()
			if int64(mfst.MemAlloc) > (availableMem[id] + int64(previousMem)) {
				msg := fmt.Sprintf("Insufficient memory for requested allocation (have %d, want %d)",
					availableMem[id]+previousMem, mfst.MemAlloc)
				log.Debugf("(%s) %s", mfst.ServiceName, msg)
				ologs[id] <- SLM{mfst.ServiceName, "[FAILURE] " + msg}
				availLocks[id].Unlock()
				return
			} else if int64(mfst.CPUShares) > (availableCPUShares[id] + int64(previousCPU)) {
				msg := fmt.Sprintf("Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)",
					availableCPUShares[id]+previousCPU, mfst.CPUShares)
				log.Debugf("(%s) %s", mfst.ServiceName, msg)
				ologs[id] <- SLM{mfst.ServiceName, "[FAILURE] " + msg}
				availLocks[id].Unlock()
				return
			} else {
				log.Debugf("(%s) Sufficient resources available, launching service", svcName)
				// Yes, mem and CPU pools can temporarily go negative here
				// This will be resolved when the old container dies and triggers cleanup
				availableMem[id] -= int64(mfst.MemAlloc)
				availableCPUShares[id] -= int64(mfst.CPUShares)
				availLocks[id].Unlock()
			}

			// Remove previous version if necessary
			if ok && existingManifest.Container != nil {
				log.Debugf("(%s) Stopping previous container to accommodate launch of new container",
					mfst.ServiceName)
				existingManifest.stopping = true
				err := StopContainer(alias, existingManifest.ServiceName, true)
				if err != nil {
					log.Errorf("(%s) Unable to remove existing service: %s", mfst.ServiceName, err)
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
			log.Infof("(%s) Booting service with Docker", mfst.ServiceName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Booting service"}
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, true)
			if err != nil {
				log.Errorf("(%s) Docker unable to restart container: %s", mfst.ServiceName, err)
				log.Errorf("(%s) Rolling back resource alloctions for new container", mfst.ServiceName)
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
			runningSvcsLocks[id].Lock()
			mfst.Container = container
			runningSvcsLocks[id].Unlock()
			msg := "Container (re)start successful"
			log.Debugf("(%s) "+msg, mfst.ServiceName)
			ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] " + msg}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case restart:
			// Restart a currently running service
			log.Debugf("(%s) State machine received restart event", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting restart"}
			mfst.restarting = true
			err := StopContainer(alias, mfst.ServiceName, false)
			if err != nil {
				mfst.restarting = false
				log.Errorf("(%s) Failed to stop existing service as part of restart", svcName)
				ologs[id] <- SLM{mfst.ServiceName, "[FAILURE] Unable to stop existing service"}
				continue
			}
			log.Debugf("(%s) Stopped existing service", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Stopped existing service"}

			log.Debugf("(%s) Restarting container with Docker", svcName)
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, false)
			if err != nil {
				log.Errorf("(%s) Docker failed to restart container", svcName)
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
			runningSvcsLocks[id].Lock()
			mfst.Container = container
			runningSvcsLocks[id].Unlock()

			log.Debugf("(%s) Docker container restart successful", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container restart successful"}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case autoRestart:
			// Auto-restart a service that has just terminated
			log.Debugf("(%s) State machine received auto restart event", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting auto-restart"}
			log.Debugf("(%s) Restarting container with Docker", svcName)
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, false)
			if err != nil {
				log.Errorf("(%s) Docker failed to restart container: %s", svcName, err)
				log.Errorf("(%s) Releasing allocated resources", svcName)
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
			runningSvcsLocks[id].Lock()
			mfst.Container = container
			runningSvcsLocks[id].Unlock()
			log.Debugf("(%s) Container auto restart successful", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container auto-restart successful"}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case resurrect:
			log.Debugf("(%s) State machine received resurrect event", svcName)
			// Try to start up a recently terminated service
			// Start by checking if we have sufficient resources
			availLocks[id].Lock()
			if int64(mfst.MemAlloc) > availableMem[id] {
				msg := fmt.Sprintf("Insufficient memory for requested allocation (have %d, want %d)", availableMem[id], mfst.MemAlloc)
				log.Debugf("(%s) %s", svcName, msg)
				ologs[id] <- SLM{mfst.ServiceName, "[FAILURE] " + msg}
				availLocks[id].Unlock()

				runningSvcsLocks[id].Lock()
				delete(runningServices[id], mfst.ServiceName)
				runningSvcsLocks[id].Unlock()
				close(*mfst.eventChan)
				depersistSvcHb(id, mfst.ServiceName)
			} else if int64(mfst.CPUShares) > availableCPUShares[id] {
				msg := fmt.Sprintf("Insufficient CPU shares for requested allocation (have %d, want %d)", availableCPUShares, mfst.CPUShares)
				log.Debugf("(%s) %s", svcName, alias)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Unlock()

				runningSvcsLocks[id].Lock()
				delete(runningServices[id], mfst.ServiceName)
				runningSvcsLocks[id].Unlock()
				close(*mfst.eventChan)
				depersistSvcHb(id, mfst.ServiceName)
			} else {
				log.Debugf("(%s) Sufficient resources available, starting service", svcName)
				availableMem[id] -= int64(mfst.MemAlloc)
				availableCPUShares[id] -= int64(mfst.CPUShares)
				availLocks[id].Unlock()
			}

			// Now restart the container
			log.Debugf("(%s) Starting container with Docker", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting to restart container"}
			container, err := RestartContainer(alias, mfst, cfgs[id].ContainerRouter, false)
			if err != nil {
				log.Errorf("(%s) Docker failed to start container: %s", svcName, err)
				msg := fmt.Sprintf("[FAILURE] Unable to (re)start container: %v", err)
				ologs[id] <- SLM{mfst.ServiceName, msg}
				availLocks[id].Lock()
				availableMem[id] += int64(mfst.MemAlloc)
				availableCPUShares[id] += int64(mfst.CPUShares)
				availLocks[id].Unlock()
				return
			}
			runningSvcsLocks[id].Lock()
			mfst.Container = container
			runningSvcsLocks[id].Unlock()

			log.Debugf("(%s) Container (re)start successful", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container (re)start successful"}
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)

		case stop:
			log.Debugf("(%s) State machine received stop event", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Attempting to stop container"}
			if mfst.Container == nil {
				log.Debugf("(%s) Service has already been stopped, no further action needed", svcName)
				ologs[id] <- SLM{mfst.ServiceName, "[INFO] Container is already stopped"}
			} else {
				// Updating available mem and cpu shares done by event monitor
				log.Debugf("(%s) Stopping container with Docker", svcName)
				err := StopContainer(alias, mfst.ServiceName, true)
				if err != nil {
					log.Errorf("(%s) Docker failed to stop container: %s", svcName, err)
					msg := fmt.Sprintf("[FAILURE] Unable to stop container: %v", err)
					ologs[id] <- SLM{mfst.ServiceName, msg}
				} else {
					mfst.stopping = true
					log.Debugf("(%s) Docker has stopped container", svcName)
					ologs[id] <- SLM{mfst.ServiceName, "[SUCCESS] Container stopped"}
				}
			}

		case die:
			log.Debugf("(%s) State machine received die event", svcName)
			if mfst.restarting {
				log.Debugf("(%s) This is part of normal restart process, ignoring", svcName)
				mfst.restarting = false
				continue
			}
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Container has died"}
			if mfst.AutoRestart && !mfst.stopping {
				log.Debugf("(%s) Initiating automatic restart in %s", svcName, mfst.RestartInt.String())
				go func() {
					if mfst.RestartInt > 0 {
						time.Sleep(mfst.RestartInt)
					}
					*mfst.eventChan <- autoRestart
				}()
			} else {
				if mfst.stopping {
					log.Debugf("(%s) This is part of manual service stop, ignoring die event", svcName)
					mfst.stopping = false
				}
				runningSvcsLocks[id].Lock()
				mfst.Container = nil
				runningSvcsLocks[id].Unlock()
				log.Debugf("(%s) Releasing service resource allocations", svcName)
				availLocks[id].Lock()
				availableCPUShares[id] += int64(mfst.CPUShares)
				availableMem[id] += int64(mfst.MemAlloc)
				availLocks[id].Unlock()

				// Defer removal of the manifest in case user wants to resurrect service
				log.Debugf("(%s) Service will remain a zombie for %s", svcName, mfst.ZombiePeriod.String())
				// Temporarily hijack state machine from the main loop
				timeout := time.NewTimer(mfst.ZombiePeriod)
				select {
				case <-timeout.C:
					// Zombie period has expired, purge service data
					log.Debugf("(%s) Zombie period expired, purging dead service manifest", svcName)
					runningSvcsLocks[id].Lock()
					delete(runningServices[id], mfst.ServiceName)
					runningSvcsLocks[id].Unlock()
					close(*mfst.eventChan)
					depersistSvcHb(id, mfst.ServiceName)

				case event = <-*mfst.eventChan:
					if event == resurrect {
						// No purging necessary, we need to restart service
						// Re-enqueue the event and let the main loop reassume control
						log.Debugf("(%s) Service will be resurrected, zombie period cancelled", svcName)
						*mfst.eventChan <- resurrect
					} else {
						// This should never happen
						log.Errorf("(%s) Received non resurrect event for state machine in zombie state, ignoring", svcName)
					}
				}
			}

		case adopt:
			log.Debugf("(%s) Reassuming ownership of running service upon spawnd restart", svcName)
			ologs[id] <- SLM{mfst.ServiceName, "[INFO] Reassuming ownership upon spawnd restart"}
			availLocks[id].Lock()
			availableMem[id] -= int64(mfst.MemAlloc)
			availableCPUShares[id] -= int64(mfst.CPUShares)
			availLocks[id].Unlock()
			go svcHeartbeat(id, mfst.ServiceName, mfst.Container.StatChan)
		}
	}
}

func svcHeartbeat(id int, svcName string, statCh *chan *docker.Stats) {
	logs[id].Debugf("(%s) Starting service heartbeat loop", svcName)
	spURI := cfgs[id].Path

	lastEmitted := time.Now()
	lastCPUPercentage := 0.0
	for stats := range *statCh {
		if stats.Read.Sub(lastEmitted).Seconds() > heartbeatPeriod {
			runningSvcsLocks[id].Lock()
			manifest, ok := runningServices[id][svcName]
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
				Name:           svcName,
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
			logs[id].Debugf("(%s) Publishing service heartbeat", svcName)

			hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointSvcHb, msg)
			if err != nil {
				logs[id].Errorf("(%s) Failed to marshal service heartbeat: %s", svcName, err)
			} else if err = spInterfaces[id].PublishSignal("heartbeat/"+svcName, hbPo); err != nil {
				logs[id].Errorf("(%s) Failed to publish service heartbeat message: %s", svcName, err)
			}

			lastEmitted = time.Now()
		}
	}
}

func depersistSvcHb(id int, svcName string) {
	// Publishing a message with no POs is effectively a "de-persist" operation
	logs[id].Debugf("(%s) De-persisting service heartbeat message", svcName)
	if err := spInterfaces[id].PublishSignal("heartbeat/" + svcName); err != nil {
		logs[id].Errorf("(%s) Failed to de-persist service heartbeat: %s", svcName, err)
	}
}

func persistManifests(id int) {
	for {
		logs[id].Debug("Saving service manifests snapshot")
		runningSvcsLocks[id].Lock()
		manifests := make([]Manifest, len(runningServices[id]))
		i := 0

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
			logs[id].Errorf("Failed to marshal manifests: %s", err)
		} else if err := ioutil.WriteFile(mfstFileDest, manifestRaw.Bytes(), 0600); err != nil {
			logs[id].Errorf("Failed to write manifest snapshot to file: %s", err)
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
		logs[i].Debugf("Attempting to recover state from previous manifests snapshot at %s", mfstFileSrc)
		mfstBytes, err := ioutil.ReadFile(mfstFileSrc)
		if err != nil {
			// This isn't a huge deal, happens e.g. for new spawnpoints
			logs[i].Warningf("Failed to read manifests snapshot: %s", err)
			return
		}

		var priorManifests []Manifest
		decoder := gob.NewDecoder(bytes.NewReader(mfstBytes))
		if err = decoder.Decode(&priorManifests); err != nil {
			logs[i].Errorf("Failed to decode manifests snapshot file: %s", err)
			continue
		} else {
			logs[i].Debugf("Successfully read manifests snapshot")
		}

		knownContainers, err := GetSpawnedContainers(cfgs[i].Alias)
		if err != nil {
			logs[i].Errorf("Failed to retrieve info on running containers from Docker")
			continue
		}

		reclaimedManifests := make(map[string]*Manifest)
		for _, mfst := range priorManifests {
			logs[i].Debugf("(%s) Attempting to restore service", mfst.ServiceName)
			thisMfst := mfst
			newEvChan := make(chan svcEvent, 5)
			thisMfst.eventChan = &newEvChan
			logger := NewLogger(bwClients[i], cfgs[i].Path, cfgs[i].Alias, thisMfst.ServiceName)
			thisMfst.logger = logger
			containerInfo, ok := knownContainers[thisMfst.ServiceName]
			if ok && containerInfo.Raw.State.Running {
				logs[i].Debugf("(%s) Service remained running, reclaiming it", mfst.ServiceName)
				thisMfst.Container = ReclaimContainer(&thisMfst, containerInfo.Raw)
				go manageService(i, &thisMfst)
				reclaimedManifests[thisMfst.ServiceName] = &thisMfst
				newEvChan <- adopt
			} else if mfst.AutoRestart {
				logs[i].Debugf("(%s) Service is no longer running, but has auto restart enabled", mfst.ServiceName)
				go manageService(i, &thisMfst)
				newEvChan <- boot
			} else {
				logs[i].Debugf("(%s) Service has died, but no auto restart was requested", mfst.ServiceName)
			}
		}
		runningSvcsLocks[i].Lock()
		for name, mfst := range reclaimedManifests {
			runningServices[i][name] = mfst
		}
		runningSvcsLocks[i].Unlock()
	}
}
