package backend

import (
	"context"
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
)

type ServiceBackend interface {
	StartService(ctx context.Context, config *service.Configuration) (string, error)
	RestartService(ctx context.Context, id string) error
	StopService(ctx context.Context, id string) error
	RemoveService(ctx context.Context, id string) error
	ListServices(ctx context.Context) ([]string, error)
	TailService(ctx context.Context, id string, log bool) (<-chan string, <-chan error)
	MonitorService(ctx context.Context, id string) (<-chan Event, <-chan error)
	ProfileService(ctx context.Context, id string, period time.Duration) (<-chan Stats, <-chan error)
}

type Event int

const (
	Die = iota
)

type Stats struct {
	Memory    float64
	CPUShares float64
}
