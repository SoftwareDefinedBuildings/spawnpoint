package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/backend"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/util"
	bw2 "github.com/immesys/bw2bind"
	"github.com/op/go-logging"
	"github.com/pkg/errors"
)

const heartbeatInterval = 10 * time.Second

type Config struct {
	BW2Entity string `yaml:"bw2Entity"`
	BW2Agent  string `yaml:"bw2Agent"`
	Path      string `yaml:"path"`
	CPUShares uint32 `yaml:"cpuShares"`
	Memory    uint32 `yaml:"memory"`
	Backend   string `yaml:"backend"`
}

type SpawnpointDaemon struct {
	bw2Service         *bw2.Service
	backend            backend.ServiceBackend
	logger             *logging.Logger
	path               string
	alias              string
	totalCPUShares     uint32
	totalMemory        uint32
	availableCPUShares uint32
	availableMemory    uint32
	resourceLock       sync.RWMutex
	serviceRegistry    map[string]*runningService
	registryLock       sync.RWMutex
}

type runningService struct {
	service.Configuration
	Log chan string
}

func New(config *Config, metadata *map[string]string, logger *logging.Logger) (*SpawnpointDaemon, error) {
	if err := validateDaemonConfig(config); err != nil {
		return nil, errors.Wrap(err, "Invalid daemon configuration")
	}

	pathElements := strings.Split(config.Path, "/")
	daemon := SpawnpointDaemon{
		logger:             logger,
		alias:              pathElements[len(pathElements)-1],
		path:               config.Path,
		totalCPUShares:     config.CPUShares,
		totalMemory:        config.Memory,
		availableCPUShares: config.CPUShares,
		availableMemory:    config.Memory,
		serviceRegistry:    make(map[string]*runningService),
	}

	if err := daemon.initBosswave(config); err != nil {
		return nil, errors.Wrap(err, "Could not initialize bosswave")
	}

	switch strings.ToLower(config.Backend) {
	case "docker", "":
		dkr, err := backend.NewDocker(daemon.alias, config.BW2Agent, logger)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to instantiate Docker backend")
		}
		daemon.backend = dkr
	default:
		return nil, fmt.Errorf("Unknown container backend: %s", config.Backend)
	}

	return &daemon, nil
}

func validateDaemonConfig(config *Config) error {
	if len(config.Path) == 0 {
		return errors.New("path is empty string")
	} else if len(config.BW2Entity) == 0 {
		return errors.New("bw2Entity is empty string")
	} else if config.CPUShares == 0 {
		return errors.New("Must allocate more than 0 CPU shares to spawnd")
	} else if config.Memory == 0 {
		return errors.New("Must allocate more than 0 MB memory to spawnd")
	}

	return nil
}

func (daemon *SpawnpointDaemon) initBosswave(config *Config) error {
	bw2.SilenceLog()

	client, err := bw2.Connect(config.BW2Agent)
	if err != nil {
		return errors.Wrap(err, "Failed to connect to Bosswave")
	}
	if _, err = client.SetEntityFile(config.BW2Entity); err != nil {
		return errors.Wrap(err, "Failed to set Bosswave entity")
	}

	service := client.RegisterService(config.Path, "s.spawnpoint")
	service.SetErrorHandler(func(err error) {
		daemon.logger.Errorf("Failed to register service metadata: %s", err)
	})
	bw2Iface := service.RegisterInterface("daemon", "i.spawnpoint")
	if err := bw2Iface.SubscribeSlot("config", daemon.handleConfig); err != nil {
		return errors.Wrap(err, "Failed to subscribe to config slot")
	}
	daemon.bw2Service = service

	return nil
}

func (daemon *SpawnpointDaemon) handleConfig(msg *bw2.SimpleMessage) {
	daemon.logger.Debug("Received new service configuration")

	if len(msg.POs) == 0 {
		daemon.logger.Debug("Received configuration has no payload objects, ignoring")
		return
	}
	configPo, ok := msg.POs[0].(bw2.YAMLPayloadObject)
	if !ok {
		daemon.logger.Debug("Received service configuration does not have msgpack payload, ignoring")
		return
	}
	var svcConfig service.Configuration
	if err := bw2.YAMLPayloadObject.ValueInto(configPo, &svcConfig); err != nil {
		daemon.logger.Debugf("Failed to parse service configuration msgpack: %s", err)
		return
	}

	daemon.logger.Debug("Attempting to start new service...")
	logChan, err := daemon.backend.StartService(context.Background(), &svcConfig)
	if err != nil {
		daemon.logger.Errorf("Failed to start service: %s", err)
		if err = daemon.publishLogMessage(&svcConfig, fmt.Sprintf("[ERROR] Failed to start service: %s", err)); err != nil {
			daemon.logger.Errorf("Failed to publish log message: %s", err)
		}
		return
	}
	daemon.logger.Debug("Service started successfully")
	if err = daemon.publishLogMessage(&svcConfig, fmt.Sprintf("[SUCCESS] Service container has started")); err != nil {
		daemon.logger.Errorf("Failed to publish log message: %s", err)
	}
	daemon.registryLock.Lock()
	daemon.serviceRegistry[svcConfig.Name] = &runningService{
		Configuration: svcConfig,
		Log:           logChan,
	}
	daemon.registryLock.Unlock()

	go daemon.tailLogs(svcConfig.Name, logChan)
}

func (daemon *SpawnpointDaemon) StartLoop(ctx context.Context) {
	daemon.logger.Debugf("Spawnpoint Daemon Version %s -- starting main loop", util.VersionNum)
	go daemon.publishHearbeats(ctx, heartbeatInterval)
	<-ctx.Done()
	daemon.logger.Debug("Main loop canceled -- terminating")
}

func (daemon *SpawnpointDaemon) tailLogs(svcName string, log chan string) {
	bw2Iface := daemon.bw2Service.RegisterInterface(svcName, "i.spawnable")
	alive := true
	pending := time.AfterFunc(1*time.Minute, func() {
		daemon.logger.Debugf("(%s) Logging timeout has expired", svcName)
		alive = false
	})
	if err := bw2Iface.SubscribeSlot("keepLogAlive", func(msg *bw2.SimpleMessage) {
		daemon.logger.Debugf("(%s) Received log keep-alive message", svcName)
		pending.Stop()
		pending.Reset(1 * time.Minute)
		alive = true
	}); err != nil {
		daemon.logger.Errorf("(%s) Failed to subscribe to log keep-alive slot: %s", svcName, err)
	}

	for message := range log {
		if alive {
			daemon.logger.Debugf("(%s) New log entry available, sending", svcName)
			po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, service.LogMessage{
				Contents:  message,
				Timestamp: time.Now().UnixNano(),
			})
			if err != nil {
				daemon.logger.Errorf("(%s) Failed to serialize log message: %s", svcName, err)
			}

			if err = bw2Iface.PublishSignal("log", po); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcName, err)
			}
		} else {
			daemon.logger.Debugf("(%s) New log entry available, but not active recipients", svcName)
		}
	}
}

func (daemon *SpawnpointDaemon) publishLogMessage(svcConfig *service.Configuration, msg string) error {
	iFace := daemon.bw2Service.RegisterInterface(svcConfig.Name, "i.spawnable")
	logMessage := service.LogMessage{
		Timestamp: time.Now().UnixNano(),
		Contents:  msg,
	}
	logMessagePo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, logMessage)
	if err != nil {
		return errors.Wrap(err, "Failed to create msgpack object")
	}

	if err = iFace.PublishSignal("log", logMessagePo); err != nil {
		return errors.Wrap(err, "Bosswave publication failed")
	}
	return nil
}
