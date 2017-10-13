package main

import (
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/objects"
	bw2 "gopkg.in/immesys/bw2bind.v5"
)

type BWLogger struct {
	bwClient  *bw2.BW2Client
	ifcClient *bw2.InterfaceClient
	spAlias   string
	svcName   string
}

func NewLogger(bwClient *bw2.BW2Client, base string, spAlias string, svcName string) *BWLogger {
	svcClient := bwClient.NewServiceClient(base, "s.spawnpoint")
	ifcClient := svcClient.AddInterface("server", "i.spawnpoint")
	logger := BWLogger{
		bwClient,
		ifcClient,
		spAlias,
		svcName,
	}

	return &logger
}

func (logger *BWLogger) Write(msg []byte) (int, error) {
	po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, objects.SPLogMsg{
		Time:     time.Now().UnixNano(),
		SPAlias:  logger.spAlias,
		Service:  logger.svcName,
		Contents: string(msg),
	})
	if err != nil {
		return 0, err
	}

	err = logger.bwClient.Publish(&bw2.PublishParams{
		URI:            logger.ifcClient.SignalURI("log"),
		PayloadObjects: []bw2.PayloadObject{po},
	})
	if err != nil {
		return 0, err
	}

	return len(msg), nil
}
