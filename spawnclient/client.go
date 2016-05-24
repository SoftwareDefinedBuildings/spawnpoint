package spawnctl

import (
	"errors"
	"time"

	"github.com/immesys/spawnpoint/objects"
	"github.com/immesys/spawnpoint/uris"

	bw2 "gopkg.in/immesys/bw2bind.v5"
	yaml "gopkg.in/yaml.v2"
)

type SpawnClient struct {
	bwClient *bw2.BW2Client
}

func NewSpawnClient(bwClient *bw2.BW2Client) *SpawnClient {
	return &SpawnClient{bwClient: bwClient}
}

func FromHeartbeat(msg *bw2.SimpleMessage) (*objects.SpawnPoint, error) {
	for _, po := range msg.POs {
		if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
			hb := objects.SpawnPointHb{}
			po.(bw2.YAMLPayloadObject).ValueInto(&hb)

			uri := msg.URI[:len(msg.URI)-len("/s.spawnpoint/server/i.spawnpoint/signal/heartbeat")]
			seen := time.Unix(0, hb.Time)

			sp := objects.SpawnPoint{uri, seen, hb.Alias, hb.AvailableCpuShares, hb.AvailableMem}
			return &sp, nil
		}
	}

	err := errors.New("Heartbeat contained no valid payload object")
	return nil, err
}

func (client *SpawnClient) DeployService(spawnPoint *objects.SpawnPoint, config *objects.SvcConfig) error {
	rawYaml, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	configPo, err := bw2.LoadYAMLPayloadObject(bw2.PONumSpawnpointConfig, rawYaml)
	if err != nil {
		return err
	}

	// Send service config to spawnpoint
	uri := uris.SlotPath(spawnPoint.URI, "config")
	err = client.bwClient.Publish(&bw2.PublishParams{
		URI:            uri,
		PayloadObjects: []bw2.PayloadObject{configPo},
	})
	if err != nil {
		return err
	}

	// Instruct spawnpoint to launch service
	uri = uris.SlotPath(spawnPoint.URI, "restart")
	namePo := bw2.CreateStringPayloadObject(config.ServiceName)

	return client.bwClient.Publish(&bw2.PublishParams{
		URI:            uri,
		PayloadObjects: []bw2.PayloadObject{namePo},
	})
}

func (client *SpawnClient) RestartService(spawnPoint *objects.SpawnPoint, svcName string) error {
	po := bw2.CreateStringPayloadObject(svcName)
	uri := uris.SlotPath(spawnPoint.URI, "ctl/restart")

	return client.bwClient.Publish(&bw2.PublishParams{
		URI:            uri,
		PayloadObjects: []bw2.PayloadObject{po},
	})
}

func (client *SpawnClient) StopService(spawnPoint *objects.SpawnPoint, svcName string) error {
	po := bw2.CreateStringPayloadObject(svcName)
	uri := uris.SlotPath(spawnPoint.URI, "ctl/stop")

	return client.bwClient.Publish(&bw2.PublishParams{
		URI:            uri,
		PayloadObjects: []bw2.PayloadObject{po},
	})
}
