package main

import (
	"time"

	"github.com/immesys/spawnpoint/objects"
	"github.com/immesys/spawnpoint/uris"
	bw2 "gopkg.in/immesys/bw2bind.v5"
)

type BWLogger struct {
	bwClient *bw2.BW2Client
	spAlias  string
	svcName  string
	uri      string
}

func NewLogger(bwClient *bw2.BW2Client, base string, spAlias string, svcName string) (*BWLogger, error) {
	logger := BWLogger{
		bwClient,
		spAlias,
		svcName,
		uris.ServiceSignalPath(base, svcName, "log"),
	}

	return &logger, nil
}

func (logger BWLogger) Write(msg []byte) (int, error) {
	po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog, objects.SPLog{
		time.Now().UnixNano(),
		logger.spAlias,
		logger.svcName,
		string(msg),
	})
	if err != nil {
		return 0, err
	}

	err = bwClient.Publish(&bw2.PublishParams{
		URI:            logger.uri,
		PayloadObjects: []bw2.PayloadObject{po},
	})
	if err != nil {
		return 0, err
	}

	return len(msg), nil
}
