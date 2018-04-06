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
const persistenceInterval = 30 * time.Second

type Config struct {
	BW2Entity            string `yaml:"bw2Entity"`
	BW2Agent             string `yaml:"bw2Agent"`
	Path                 string `yaml:"path"`
	CPUShares            uint64 `yaml:"cpuShares"`
	Memory               uint64 `yaml:"memory"`
	Backend              string `yaml:"backend"`
	EnableHostNetworking bool   `yaml:"enableHostNetworking"`
	EnableDeviceMapping  bool   `yaml:"enableDeviceMapping"`
}

type SpawnpointDaemon struct {
	Config
	bw2Client          *bw2.BW2Client
	bw2Service         *bw2.Service
	backend            backend.ServiceBackend
	logger             *logging.Logger
	alias              string
	availableCPUShares uint64
	availableMemory    uint64
	resourceLock       sync.RWMutex
	serviceRegistry    map[string]*serviceManifest
	registryLock       sync.RWMutex
}

type serviceManifest struct {
	*service.Configuration
	ID     string
	Events chan service.Event
}

func New(config *Config, logger *logging.Logger) (*SpawnpointDaemon, error) {
	if err := validateDaemonConfig(config); err != nil {
		return nil, errors.Wrap(err, "Invalid daemon configuration")
	}

	pathElements := strings.Split(config.Path, "/")
	daemon := SpawnpointDaemon{
		Config:             *config,
		logger:             logger,
		alias:              pathElements[len(pathElements)-1],
		availableCPUShares: config.CPUShares,
		availableMemory:    config.Memory,
		serviceRegistry:    make(map[string]*serviceManifest),
	}

	if err := daemon.initBosswave(config); err != nil {
		return nil, errors.Wrap(err, "Could not initialize bosswave")
	}

	switch strings.ToLower(config.Backend) {
	case "docker", "":
		dkr, err := backend.NewDocker(daemon.alias, config.BW2Agent)
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

	service := client.RegisterServiceNoHb(config.Path, "s.spawnpoint")
	service.SetErrorHandler(func(err error) {
		daemon.logger.Errorf("Failed to register service metadata: %s", err)
	})
	bw2Iface := service.RegisterInterface("daemon", "i.spawnpoint")
	if err := bw2Iface.SubscribeSlot("config", daemon.handleConfig); err != nil {
		return errors.Wrap(err, "Failed to subscribe to config slot")
	}
	daemon.bw2Client = client
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

	daemon.registryLock.RLock()
	_, ok = daemon.serviceRegistry[svcConfig.Name]
	daemon.registryLock.RUnlock()
	if ok {
		daemon.logger.Debugf("(%s) Service is already running, ignoring deploy command", svcConfig.Name)
		if err := daemon.publishLogMessage(svcConfig.Name, "[ERROR] Service is already running on this host"); err != nil {
			daemon.logger.Errorf("(%s) Failed to publish log message", svcConfig.Name)
		}
		return
	}

	if svcConfig.UseHostNet && !daemon.EnableHostNetworking {
		daemon.logger.Debugf("(%s) Configuration requests use of host network, which is disabled", svcConfig.Name)
		msg := "[ERROR] Use of host networking stack not allowed on this host"
		if err := daemon.publishLogMessage(svcConfig.Name, msg); err != nil {
			daemon.logger.Errorf("(%s) Failed to publish log message", svcConfig.Name)
		}
		return
	} else if len(svcConfig.Devices) > 0 && !daemon.EnableDeviceMapping {
		daemon.logger.Debugf("(%s) Configuration requests device mapping(s), which are disabled", svcConfig.Name)
		msg := "[ERROR] Mapping devices into container not allowed on this host"
		if err := daemon.publishLogMessage(svcConfig.Name, msg); err != nil {
			daemon.logger.Errorf("(%s) Failed to publish log message", svcConfig.Name)
		}
		return
	}

	svc := serviceManifest{Configuration: &svcConfig}
	daemon.addService(&svc, true)
}

func (daemon *SpawnpointDaemon) addService(svc *serviceManifest, boot bool) {
	svc.Events = make(chan service.Event, 1)
	done := make(chan struct{})
	go daemon.manageService(svc, done)
	if boot {
		svc.Events <- service.Boot
	} else {
		svc.Events <- service.Adopt
	}

	bw2Iface := daemon.bw2Service.RegisterInterface(svc.Name, "i.spawnable")
	restartUnsubHandle, err := bw2Iface.SubscribeSlotH("restart", daemon.manipulateService(svc.Name, "restart", done))
	if err != nil {
		daemon.logger.Errorf("(%s) Failed to subscribe to restart slot: %s", svc.Name, err)
		return
	}
	stopUnsubHandle, err := bw2Iface.SubscribeSlotH("stop", daemon.manipulateService(svc.Name, "stop", done))
	if err != nil {
		daemon.logger.Errorf("(%s) Failed to subscribe to stop slot: %s", svc.Name, err)
		return
	}
	go func() {
		<-done
		if err := daemon.bw2Client.Unsubscribe(restartUnsubHandle); err != nil {
			daemon.logger.Errorf("(%s) Failed to unsubscribe from restart slot", svc.Name)
		} else {
			daemon.logger.Debugf("(%s) Unsubscribed from restart slot", svc.Name)
		}
		if err := daemon.bw2Client.Unsubscribe(stopUnsubHandle); err != nil {
			daemon.logger.Errorf("(%s) Failed to unsubscribe from stop slot", svc.Name)
		} else {
			daemon.logger.Debugf("(%s) Unsubscribed from stop slot", svc.Name)
		}
	}()
}

func (daemon *SpawnpointDaemon) manipulateService(name string, operation string, done <-chan struct{}) func(*bw2.SimpleMessage) {
	return func(msg *bw2.SimpleMessage) {
		daemon.logger.Debugf("(%s) Received service manipulation command", name)
		// We want to ignore "messages" fired by an unsubscribe
		select {
		case <-done:
			daemon.logger.Debugf("(%s) Service no longer running, message probably generated by an unsubscribe", name)
			return
		default:
		}

		daemon.registryLock.RLock()
		svc, ok := daemon.serviceRegistry[name]
		daemon.registryLock.RUnlock()
		if !ok {
			daemon.logger.Debugf("(%s) Service not found, ignoring command", name)
			daemon.publishLogMessage(name, "[ERROR] Service not found")
			return
		}

		switch operation {
		case "restart":
			daemon.logger.Debugf("%s) Issuing restart event", name)
			svc.Events <- service.Restart
		case "stop":
			daemon.logger.Debugf("(%s) Issuing stop event", name)
			svc.Events <- service.Stop
		default:
			daemon.logger.Warningf("(%s) Unknown operation type %s", name, operation)
		}
	}
}

func (daemon *SpawnpointDaemon) StartLoop(ctx context.Context) {
	daemon.logger.Debugf("Spawnpoint Daemon Version %s -- starting main loop", util.VersionNum)
	if err := daemon.recoverServices(ctx); err != nil {
		daemon.logger.Errorf("Failed to recover from previous service snapshot: %s", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		daemon.publishHearbeats(ctx, heartbeatInterval)
		wg.Done()
	}()
	go func() {
		daemon.persistSnapshots(ctx, persistenceInterval)
		wg.Done()
	}()
	wg.Wait()
	daemon.logger.Debug("Main loop canceled -- terminating")
}
