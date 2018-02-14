package backend

import (
	"context"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
)

type ServiceBackend interface {
	StartService(ctx context.Context, config *service.Configuration) (chan string, error)
	RestartService(ctx context.Context, id string) error
	StopService(ctx context.Context, id string) error
}
