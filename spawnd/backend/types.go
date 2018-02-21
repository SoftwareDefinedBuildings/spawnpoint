package backend

import (
	"context"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
)

type ServiceBackend interface {
	StartService(ctx context.Context, config *service.Configuration) (string, error)
	RestartService(ctx context.Context, id string) error
	StopService(ctx context.Context, id string) error
	RemoveService(ctx context.Context, id string) error
	TailService(ctx context.Context, id string, log bool) (<-chan string, <-chan error)
	MonitorService(ctx context.Context, id string) (<-chan Event, <-chan error)
}

type Event int

const (
	Die = iota
)
