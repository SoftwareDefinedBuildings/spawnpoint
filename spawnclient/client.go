package spawnclient

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/immesys/spawnpoint/objects"
	"github.com/immesys/spawnpoint/uris"
	"github.com/jhoonb/archivex"
	bw2 "gopkg.in/immesys/bw2bind.v5"
)

type SpawnClient struct {
	bwClient *bw2.BW2Client
}

func New(router string, entityFile string) (*SpawnClient, error) {
	BWC, err := bw2.Connect(router)
	if err != nil {
		return nil, err
	}

	_, err = BWC.SetEntityFile(entityFile)
	if err != nil {
		return nil, err
	}

	return &SpawnClient{bwClient: BWC}, nil
}

func (sc *SpawnClient) Scan(baseURI string) (map[string]objects.SpawnPoint, error) {
	scanuri := uris.SignalPath(baseURI, "heartbeat")
	heartbeatMsgs, err := sc.bwClient.Query(&bw2.QueryParams{URI: scanuri})
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

func (sc *SpawnClient) Inspect(spawnpointURI string) ([]objects.Service, error) {
	inspectURI := uris.ServiceSignalPath(spawnpointURI, "*", "heartbeat")
	svcHbMsgs, err := sc.bwClient.Query(&bw2.QueryParams{URI: inspectURI})
	if err != nil {
		return nil, err
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

				newService := objects.Service{
					Name:      svcHb.Name,
					HostURI:   svcHb.SpawnpointURI,
					LastSeen:  time.Unix(0, svcHb.Time),
					MemAlloc:  svcHb.MemAlloc,
					CPUShares: svcHb.CPUShares,
				}
				svcs = append(svcs, newService)
			}
		}
	}

	return svcs, nil
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
	subURI := uris.ServiceSignalPath(baseURI, name, "log")
	rawLog, err := sc.bwClient.Subscribe(&bw2.SubscribeParams{URI: subURI})
	if err != nil {
		return nil, fmt.Errorf("Failed to suscribe to service log: %v", err)
	}
	log := make(chan *objects.SPLogMsg)
	go func() {
		for rawMsg := range rawLog {
			if spLogPo := rawMsg.GetOnePODF(bw2.PODFSpawnpointLog); spLogPo != nil {
				var logMsg objects.SPLogMsg
				if err = spLogPo.(bw2.MsgPackPayloadObject).ValueInto(&logMsg); err == nil {
					log <- &logMsg
				}
			}
		}
	}()

	if cmd == "restart" || cmd == "stop" {
		po := bw2.CreateStringPayloadObject(name)
		err = sc.bwClient.Publish(&bw2.PublishParams{
			URI:            uris.SlotPath(baseURI, cmd),
			PayloadObjects: []bw2.PayloadObject{po},
		})
		if err != nil {
			return nil, fmt.Errorf("Failed to publish request: %v", err)
		}
	}

	return log, nil
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

func (sc *SpawnClient) DeployService(config *objects.SvcConfig, spURI string, name string) (chan *objects.SPLogMsg, error) {
	var err error
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

	// Prepare payload objects
	pos := make([]bw2.PayloadObject, 1)
	configPo, err := bw2.CreateYAMLPayloadObject(bw2.PONumSpawnpointConfig, config)
	if err != nil {
		return nil, fmt.Errorf("Failed to serialize configuration: %v", err)
	}
	pos[0] = configPo
	includeEnc, err := createArchiveEncoding(config.IncludedDirs, config.IncludedFiles)
	if err != nil {
		return nil, fmt.Errorf("Failed to encode included files: %v", err)
	}
	if includeEnc != "" {
		pos = append(pos, bw2.CreateStringPayloadObject(includeEnc))
	}

	// Prepare channel to tail service's log
	logURI := uris.ServiceSignalPath(spURI, config.ServiceName, "log")
	rawLog, err := sc.bwClient.Subscribe(&bw2.SubscribeParams{URI: logURI})
	if err != nil {
		return nil, fmt.Errorf("Failed to subscribe to service log: %v", err)
	}
	log := make(chan *objects.SPLogMsg)
	go func() {
		for rawMsg := range rawLog {
			logPo := rawMsg.GetOnePODF(bw2.PODFSpawnpointLog)
			if logPo != nil {
				var logMsg objects.SPLogMsg
				if err := logPo.(bw2.MsgPackPayloadObject).ValueInto(&logMsg); err == nil {
					log <- &logMsg
				}
			}
		}
	}()

	configURI := uris.SlotPath(spURI, "config")
	if err := sc.bwClient.Publish(&bw2.PublishParams{URI: configURI, PayloadObjects: pos}); err != nil {
		return nil, fmt.Errorf("Failed to publish config to spawnpoint: %v", err)
	}

	return log, nil
}
