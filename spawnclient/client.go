package spawnclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"io/ioutil"
	"path/filepath"
	"strings"
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/daemon"
	bw2 "github.com/immesys/bw2bind"
	"github.com/mholt/archiver"
	"github.com/pkg/errors"
)

type SpawnClient struct {
	bwClient *bw2.BW2Client
}

func New(router, entityFile string) (*SpawnClient, error) {
	bw2.SilenceLog()
	client, err := bw2.Connect(router)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to connect to BW2")
	}
	if _, err := client.SetEntityFile(entityFile); err != nil {
		return nil, errors.Wrap(err, "Failed to set BW2 entity file")
	}

	return &SpawnClient{bwClient: client}, nil
}

func (sc *SpawnClient) Scan(baseURI string) (map[string]daemon.Heartbeat, error) {
	svcClient := sc.bwClient.NewServiceClient(baseURI, "s.spawnpoint")
	iFaceClient := svcClient.AddInterface("daemon", "i.spawnpoint")
	heartbeatMsgs, err := sc.bwClient.Query(&bw2.QueryParams{
		URI: iFaceClient.SignalURI("heartbeat"),
	})
	if err != nil {
		return nil, errors.Wrap(err, "Bosswave query failed")
	}

	spawnpoints := make(map[string]daemon.Heartbeat)
	for msg := range heartbeatMsgs {
		for _, po := range msg.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
				var hb daemon.Heartbeat
				if err := po.(bw2.MsgPackPayloadObject).ValueInto(&hb); err != nil {
					// Ignore this query result
					continue
				}
				uri := msg.URI[:len(msg.URI)-len("/s.spawnpoint/daemon/i.spawnpoint/signal/heartbeat")]
				spawnpoints[uri] = hb
			}
		}
	}

	return spawnpoints, nil
}

func (sc *SpawnClient) Inspect(uri string) (*daemon.Heartbeat, map[string]daemon.ServiceHeartbeat, error) {
	daemonHbs, err := sc.Scan(uri)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Initial spawnpoint scan failed")
	} else if len(daemonHbs) == 0 {
		return nil, nil, errors.New("No spawnpoints found at URI")
	} else if len(daemonHbs) > 1 {
		return nil, nil, errors.New("Multiple spawnpoints found at URI")
	}
	// This loop is guaranteed to iterate just once
	var daemonHb daemon.Heartbeat
	for _, hb := range daemonHbs {
		daemonHb = hb
	}

	svcClient := sc.bwClient.RegisterService(uri, "s.spawnpoint")
	iFaceClient := svcClient.RegisterInterface("+", "i.spawnable")
	svcHeartbeatMsgs, err := sc.bwClient.Query(&bw2.QueryParams{
		URI: iFaceClient.SignalURI("heartbeat"),
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "Bosswave query failed")
	}
	svcHeartbeats := make(map[string]daemon.ServiceHeartbeat)
	for svcHbMsg := range svcHeartbeatMsgs {
		for _, po := range svcHbMsg.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointSvcHb) {
				var svcHb daemon.ServiceHeartbeat
				if err := po.(bw2.MsgPackPayloadObject).ValueInto(&svcHb); err != nil {
					// Ignore this query result
					continue
				}
				tokens := strings.Split(svcHbMsg.URI, "/")
				svcName := tokens[len(tokens)-4]
				svcHeartbeats[svcName] = svcHb
			}
		}
	}

	return &daemonHb, svcHeartbeats, nil
}

func (sc *SpawnClient) Deploy(config *service.Configuration, uri string) error {
	if err := validateConfig(config); err != nil {
		return errors.Wrap(err, "Invalid service configuration")
	}

	encodedEntity, err := encodeEntityFile(config.BW2Entity)
	if err != nil {
		return errors.Wrap(err, "Could not encode BW2 entity")
	}
	config.BW2Entity = encodedEntity

	if len(config.IncludedFiles) > 0 {
		encodedFiles, err := encodeIncludedFiles(config.IncludedFiles, config.IncludedDirectories)
		if err != nil {
			return errors.Wrap(err, "Could not encode included files for transmission")
		}
		config.IncludedFiles = append(config.IncludedFiles, encodedFiles)
	}

	svcClient := sc.bwClient.NewServiceClient(uri, "s.spawnpoint")
	ifaceClient := svcClient.AddInterface("daemon", "i.spawnpoint")
	configPo, err := bw2.CreateYAMLPayloadObject(bw2.PONumSpawnpointConfig, config)
	if err != nil {
		return errors.Wrap(err, "Could not serialize service configuration")
	}
	if err := ifaceClient.PublishSlot("config", configPo); err != nil {
		return errors.Wrap(err, "Could not publish service configuration")
	}

	return nil
}

func (sc *SpawnClient) Stop(uri string, svcName string) error {
	svcClient := sc.bwClient.NewServiceClient(uri, "s.spawnpoint")
	iFaceClient := svcClient.AddInterface(svcName, "i.spawnable")
	if err := iFaceClient.PublishSlot("stop"); err != nil {
		return errors.Wrap(err, "Could not publish to stop slot")
	}
	return nil
}

func (sc *SpawnClient) Restart(uri string, svcName string) error {
	svcClient := sc.bwClient.NewServiceClient(uri, "s.spawnpoint")
	iFaceClient := svcClient.AddInterface(svcName, "i.spawnable")
	if err := iFaceClient.PublishSlot("restart"); err != nil {
		return errors.Wrap(err, "Could not publish to restart slot")
	}
	return nil
}

func (sc *SpawnClient) Tail(ctx context.Context, svcName string, uri string) (<-chan service.LogMessage, <-chan error) {
	svcClient := sc.bwClient.NewServiceClient(uri, "s.spawnpoint")
	iFaceClient := svcClient.AddInterface(svcName, "i.spawnable")
	errChan := make(chan error, 1)
	logChan := make(chan service.LogMessage, 20)

	if err := iFaceClient.SubscribeSignal("log", func(msg *bw2.SimpleMessage) {
		if len(msg.POs) > 0 {
			messagePo, ok := msg.POs[0].(bw2.MsgPackPayloadObject)
			if !ok {
				return
			}
			var logMessage service.LogMessage
			if err := bw2.MsgPackPayloadObject.ValueInto(messagePo, &logMessage); err != nil {
				return
			}
			logChan <- logMessage
		}
	}); err != nil {
		close(logChan)
		errChan <- errors.Wrap(err, "Failed to subscribe to service log")
		return logChan, errChan
	}

	tick := time.Tick(30 * time.Second)
	// Publish keep-alive log messages
	go func() {
		if err := iFaceClient.PublishSlot("keepLogAlive"); err != nil {
			close(logChan)
			errChan <- errors.Wrap(err, "Failed to publish log keep-alive message")
			return
		}
		for {
			select {
			case <-tick:
				if err := iFaceClient.PublishSlot("keepLogAlive"); err != nil {
					close(logChan)
					errChan <- errors.Wrap(err, "Failed to publish log keep-alive message")
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return logChan, errChan
}

func encodeEntityFile(fileName string) (string, error) {
	absPath, _ := filepath.Abs(fileName)
	contents, err := ioutil.ReadFile(absPath)
	if err != nil {
		return "", errors.Wrap(err, "Failed to read BW2 entity file")
	}
	return base64.StdEncoding.EncodeToString(contents), nil
}

func validateConfig(config *service.Configuration) error {
	if config.BW2Entity == "" {
		return errors.New("Configuration does not specify BW2 entity")
	} else if config.CPUShares == 0 {
		return errors.New("Invalid CPU shares allocation")
	} else if config.Memory == 0 {
		return errors.New("Invalid memory allocation")
	}

	return nil
}

func encodeIncludedFiles(includedFiles []string, includedDirs []string) (string, error) {
	var buffer bytes.Buffer
	if err := archiver.Tar.Write(&buffer, includedFiles); err != nil {
		return "", errors.Wrap(err, "Could not archive included files")
	}
	if err := archiver.Tar.Write(&buffer, includedDirs); err != nil {
		return "", errors.Wrap(err, "Could not archive included directories")
	}

	contents := base64.StdEncoding.EncodeToString(buffer.Bytes())
	return contents, nil
}
