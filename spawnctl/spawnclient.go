package main

import (
	"errors"
	"time"

	bw2 "gopkg.in/immesys/bw2bind.v1"
	yaml "gopkg.in/yaml.v2"
)

type SpawnPointHb struct {
	Alias              string
	Time               string
	AvailableMem       uint64
	AvailableCpuShares uint64
}

type SvcConfig struct {
	ServiceName string
	Entity      string
	Container   string
	Build       string
	Source      string
	Params      string
	MemAlloc    string
	CpuShares   uint64
}

func FromHeartbeat(msg *bw2.SimpleMessage) (SpawnPoint, error) {
	for _, po := range msg.POs {
		if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
			hb := SpawnPointHb{}
			po.(bw2.YAMLPayloadObject).ValueInto(&hb)

			uri := msg.URI
			uri = uri[:len(uri)-len("info/spawn/!heartbeat")]

			ls, err := time.Parse(time.RFC3339, hb.Time)
			if err != nil {
				return SpawnPoint{}, err
			}

			sp := SpawnPoint{uri, ls, hb.Alias, hb.AvailableCpuShares, hb.AvailableMem}
			return sp, nil
		}
	}

	err := errors.New("Heartbeat contained no valid payload object")
	return SpawnPoint{}, err
}

func (sp *SpawnPoint) DeployService(client *bw2.BW2Client, config *SvcConfig) error {
	rawYaml, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	configPo, err := bw2.LoadYAMLPayloadObject(bw2.PONumSpawnpointConfig, rawYaml)
	if err != nil {
		return err
	}

	// Send service config to spawnpoint
	uri := sp.URI + "ctl/cfg"
	err = client.Publish(&bw2.PublishParams{
		URI:            uri,
		PayloadObjects: []bw2.PayloadObject{configPo},
		AutoChain:      true,
	})
	if err != nil {
		return err
	}

	// Instruct spawnpoint to launch service
	uri = sp.URI + "ctl/restart"
	namePo := bw2.CreateStringPayloadObject(config.ServiceName)
	err = client.Publish(&bw2.PublishParams{
		URI:            uri,
		PayloadObjects: []bw2.PayloadObject{namePo},
		AutoChain:      true,
	})
	if err != nil {
		return err
	}

	return nil
}
