package spawnclient

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/immesys/spawnpoint/objects"
	"github.com/jhoonb/archivex"
	bw2 "gopkg.in/immesys/bw2bind.v5"
)

type SpawnClient struct {
	bwClient *bw2.BW2Client
}

func New(router string, entityFile string) (*SpawnClient, error) {
	bw2.SilenceLog()
	BWC, err := bw2.Connect(router)
	if err != nil {
		return nil, err
	}
	if _, err := BWC.SetEntityFile(entityFile); err != nil {
		return nil, err
	}

	return &SpawnClient{bwClient: BWC}, nil
}

func (sc *SpawnClient) BWStatus() {
	sc.bwClient.StatLine()
}

func (sc *SpawnClient) newIfcClient(spURI string) (*bw2.ServiceClient, *bw2.InterfaceClient) {
	svcClient := sc.bwClient.NewServiceClient(spURI, "s.spawnpoint")
	ifcClient := svcClient.AddInterface("server", "i.spawnpoint")
	return svcClient, ifcClient
}

func (sc *SpawnClient) Scan(baseURI string) (map[string]objects.SpawnPoint, error) {
	svcClient := sc.bwClient.NewServiceClient(baseURI, "s.spawnpoint")
	ifcClient := svcClient.AddInterface("server", "i.spawnpoint")
	heartbeatMsgs, err := sc.bwClient.Query(&bw2.QueryParams{
		URI: ifcClient.SignalURI("heartbeat"),
	})
	if err != nil {
		return nil, err
	}

	spawnpoints := make(map[string]objects.SpawnPoint)
	for heartbeatMsg := range heartbeatMsgs {
		for _, po := range heartbeatMsg.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
				heartbeat := objects.SpawnPointHb{}
				if err = po.(bw2.MsgPackPayloadObject).ValueInto(&heartbeat); err != nil {
					continue
				}

				uri := heartbeatMsg.URI[:len(heartbeatMsg.URI)-
					len("/s.spawnpoint/server/i.spawnpoint/signal/heartbeat")]
				timestamp := time.Unix(0, heartbeat.Time)
				sp := objects.SpawnPoint{
					URI:                uri,
					LastSeen:           timestamp,
					Alias:              heartbeat.Alias,
					AvailableMem:       heartbeat.AvailableMem,
					AvailableCPUShares: heartbeat.AvailableCPUShares,
				}

				spawnpoints[heartbeat.Alias] = sp
			}
		}
	}

	return spawnpoints, nil
}

func (sc *SpawnClient) Inspect(spawnpointURI string) ([]objects.Service, map[string]*bw2.MetadataTuple, error) {
	svcClient, ifcClient := sc.newIfcClient(spawnpointURI)

	svcHbMsgs, err := sc.bwClient.Query(&bw2.QueryParams{
		URI: ifcClient.SignalURI("heartbeat/*"),
	})
	if err != nil {
		return nil, nil, err
	}
	var svcs []objects.Service
	for svcHbMsg := range svcHbMsgs {
		for _, po := range svcHbMsg.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointSvcHb) {
				svcHb := objects.SpawnpointSvcHb{}
				err = po.(bw2.MsgPackPayloadObject).ValueInto(&svcHb)
				if err != nil {
					fmt.Println("Received malformed service heartbeat:", err)
					continue
				}

				usedCPUShares := uint64(math.Ceil(svcHb.CPUPercent * objects.SharesPerCore))
				newService := objects.Service{
					Name:           svcHb.Name,
					HostURI:        svcHb.SpawnpointURI,
					LastSeen:       time.Unix(0, svcHb.Time),
					MemAlloc:       svcHb.MemAlloc,
					CPUShares:      svcHb.CPUShares,
					MemUsage:       svcHb.MemUsage,
					CPUShareUsage:  usedCPUShares,
					OriginalConfig: svcHb.OriginalConfig,
				}
				svcs = append(svcs, newService)
			}
		}
	}

	metadata, err := svcClient.GetMetadata()
	if err != nil {
		return nil, nil, err
	}

	return svcs, metadata, nil
}

func (sc *SpawnClient) InspectService(spawnpointURI string, svcName string) (*objects.Service, error) {
	_, ifcClient := sc.newIfcClient(spawnpointURI)
	svcHbMsg, err := sc.bwClient.QueryOne(&bw2.QueryParams{
		URI: ifcClient.SignalURI("heartbeat/" + svcName),
	})
	if err != nil {
		return nil, err
	}

	for _, po := range svcHbMsg.POs {
		if po.IsTypeDF(bw2.PODFSpawnpointSvcHb) {
			svcHb := objects.SpawnpointSvcHb{}
			err = po.(bw2.MsgPackPayloadObject).ValueInto(&svcHb)
			if err != nil {
				return nil, fmt.Errorf("Received malformed service heartbeat: %v", err)
			}

			usedCPUShares := uint64(math.Ceil(svcHb.CPUPercent * objects.SharesPerCore))
			newService := objects.Service{
				Name:           svcHb.Name,
				HostURI:        svcHb.SpawnpointURI,
				LastSeen:       time.Unix(0, svcHb.Time),
				MemAlloc:       svcHb.MemAlloc,
				CPUShares:      svcHb.CPUShares,
				MemUsage:       svcHb.MemUsage,
				CPUShareUsage:  usedCPUShares,
				OriginalConfig: svcHb.OriginalConfig,
			}
			return &newService, nil
		}
	}
	return nil, errors.New("Received malformed service heartbeat")
}

func (sc *SpawnClient) RestartService(baseURI string, name string) (chan *objects.SPLogMsg, error) {
	return sc.manipulateService(baseURI, name, "restart")
}

func (sc *SpawnClient) StopService(baseURI string, name string) (chan *objects.SPLogMsg, error) {
	return sc.manipulateService(baseURI, name, "stop")
}

func (sc *SpawnClient) TailService(baseURI string, name string) (chan *objects.SPLogMsg, error) {
	return sc.manipulateService(baseURI, name, "tail")
}

func (sc *SpawnClient) manipulateService(baseURI string, name string, cmd string) (chan *objects.SPLogMsg, error) {
	log := make(chan *objects.SPLogMsg)
	callback := func(msg *bw2.SimpleMessage) {
		if spLogPo := msg.GetOnePODF(bw2.PODFSpawnpointLog); spLogPo != nil {
			var logMsg objects.SPLogMsg
			err := spLogPo.(bw2.MsgPackPayloadObject).ValueInto(&logMsg)
			if err == nil && logMsg.Service == name {
				log <- &logMsg
			}
		}
	}

	_, spInterface := sc.newIfcClient(baseURI)
	err := spInterface.SubscribeSignal("log", callback)
	if err != nil {
		return nil, fmt.Errorf("Failed to subcribe to service log: %v", err)
	}

	if cmd == "restart" || cmd == "stop" {
		po := bw2.CreateStringPayloadObject(name)
		err = spInterface.PublishSlot(cmd, po)
		if err != nil {
			return nil, fmt.Errorf("Failed to publish request: %v", err)
		}
	}

	return log, nil
}

func validateConfiguration(config *objects.SvcConfig) error {
	if config.Entity == "" {
		return errors.New("Must specify a Bosswave entity")
	} else if config.MemAlloc == "" {
		return errors.New("Must specify memory allocation")
	} else if config.CPUShares == 0 {
		return errors.New("Must specify CPU shares")
	}
	return nil
}

func encodeEntityFile(fileName string) (string, error) {
	absPath, _ := filepath.Abs(fileName)
	contents, err := ioutil.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(contents), nil
}

func createArchiveEncoding(includedDirs []string, includedFiles []string) (string, error) {
	includedItems := 0
	tarFileName := os.TempDir() + "/sp_include.tar"
	tar := new(archivex.TarFile)
	tar.Create(tarFileName)

	for _, dirName := range includedDirs {
		absPath, _ := filepath.Abs(dirName)
		if _, err := os.Stat(absPath); err == nil {
			if err = tar.AddAll(absPath, false); err != nil {
				return "", err
			}
			includedItems++
		}
	}
	for _, fileName := range includedFiles {
		absPath, _ := filepath.Abs(fileName)
		if _, err := os.Stat(absPath); err == nil {
			if err = tar.AddFile(absPath); err != nil {
				return "", err
			}
			includedItems++
		}
	}

	if err := tar.Close(); err != nil {
		return "", err
	}
	defer os.Remove(tarFileName)
	if includedItems == 0 {
		return "", nil
	}
	rawBytes, err := ioutil.ReadFile(tarFileName)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(rawBytes), nil
}

func (sc *SpawnClient) DeployService(rawConfig string, config *objects.SvcConfig, spURI string, name string) (chan *objects.SPLogMsg, error) {
	err := validateConfiguration(config)
	if err != nil {
		return nil, fmt.Errorf("Invalid config file: %v", err)
	}

	if config.Entity, err = encodeEntityFile(config.Entity); err != nil {
		return nil, fmt.Errorf("Failed to encode entity file: %v", err)
	}

	// Look up target spawnpoint
	spAlias := spURI[strings.LastIndex(spURI, "/")+1:]
	spawnpoints, err := sc.Scan(spURI)
	if err != nil {
		return nil, fmt.Errorf("Initial spawnpoint scan failed: %v", err)
	}
	spawnpoint, ok := spawnpoints[spAlias]
	if !ok {
		return nil, fmt.Errorf("Spawnpoint %s not found", spURI)
	}
	if !spawnpoint.Good() {
		return nil, fmt.Errorf("Spawnpoint %s appears down", spURI)
	}

	// Prepare channel to tail service's log
	_, ifcClient := sc.newIfcClient(spURI)
	log := make(chan *objects.SPLogMsg)
	callback := func(msg *bw2.SimpleMessage) {
		if logPo := msg.GetOnePODF(bw2.PODFSpawnpointLog); logPo != nil {
			var logMsg objects.SPLogMsg
			err = logPo.(bw2.MsgPackPayloadObject).ValueInto(&logMsg)
			if err == nil && logMsg.Service == name {
				log <- &logMsg
			}
		}
	}
	if err = ifcClient.SubscribeSignal("log", callback); err != nil {
		return nil, fmt.Errorf("Failed to subscribe to log: %v", err)
	}

	// Prepare payload objects
	pos := make([]bw2.PayloadObject, 2)
	configPo, err := bw2.CreateYAMLPayloadObject(bw2.PONumSpawnpointConfig, config)
	if err != nil {
		return nil, fmt.Errorf("Failed to serialize configuration: %v", err)
	}
	pos[0] = configPo
	pos[1] = bw2.CreateStringPayloadObject(rawConfig)

	includeEnc, err := createArchiveEncoding(config.IncludedDirs, config.IncludedFiles)
	if err != nil {
		return nil, fmt.Errorf("Failed to encode included files: %v", err)
	}
	if includeEnc != "" {
		pos = append(pos, bw2.CreateStringPayloadObject(includeEnc))
	}

	if err = ifcClient.PublishSlot("config", pos...); err != nil {
		return nil, fmt.Errorf("Failed to publish configuration: %v", err)
	}
	return log, nil
}
