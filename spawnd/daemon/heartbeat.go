package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/util"
	bw2 "github.com/immesys/bw2bind"
	"github.com/pkg/errors"
)

type Heartbeat struct {
	Version         string
	Time            int64
	TotalMemory     uint64
	TotalCPU        uint64
	AvailableMemory uint64
	AvailableCPU    uint64
	Services        []string
}

type ServiceHeartbeat struct {
	Time          int64
	Memory        uint64
	CPUShares     uint64
	UsedMemory    float64
	UsedCPUShares float64
}

func (daemon *SpawnpointDaemon) publishHearbeats(ctx context.Context, delay time.Duration) {
	tick := time.Tick(delay)
	daemon.publishHeartbeatAux()
	for {
		select {
		case <-ctx.Done():
			daemon.logger.Debug("Terminating daemon heartbeat publication")
			return

		case <-tick:
			daemon.publishHeartbeatAux()
		}
	}
}

func (daemon *SpawnpointDaemon) publishHeartbeatAux() {
	bw2Iface := daemon.bw2Service.RegisterInterface("daemon", "i.spawnpoint")

	daemon.resourceLock.RLock()
	availableCPU := daemon.availableCPUShares
	availableMemory := daemon.availableMemory
	daemon.resourceLock.RUnlock()
	daemon.logger.Debug("Publishing daemon heartbeat")
	daemon.logger.Debugf("CPU: %v/%v, Memory: %v/%v", availableCPU, daemon.CPUShares,
		availableMemory, daemon.Memory)

	services := make([]string, len(daemon.serviceRegistry))
	daemon.registryLock.RLock()
	i := 0
	for name := range daemon.serviceRegistry {
		services[i] = name
		i++
	}
	daemon.registryLock.RUnlock()

	hb := Heartbeat{
		Version:         util.VersionNum,
		Time:            time.Now().UnixNano(),
		TotalCPU:        daemon.CPUShares,
		TotalMemory:     daemon.Memory,
		AvailableCPU:    availableCPU,
		AvailableMemory: availableMemory,
		Services:        services,
	}
	hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointHeartbeat, hb)
	if err != nil {
		daemon.logger.Errorf("Failed to marshal heartbeat: %s", err)
	} else if err := bw2Iface.PublishSignal("heartbeat", hbPo); err != nil {
		daemon.logger.Errorf("Failed to publish daemon heartbeat: %s", err)
	}
}

func (daemon *SpawnpointDaemon) publishServiceHeartbeats(ctx context.Context, svc *serviceManifest, period time.Duration, wg *sync.WaitGroup) {
	defer wg.Done()
	statChan, errChan := daemon.backend.ProfileService(ctx, svc.ID, period)
	bw2Iface := daemon.bw2Service.RegisterInterface(svc.Name, "i.spawnable")
	for stats := range statChan {
		daemon.logger.Debugf("(%s) Publishing service heartbeat", svc.Name)
		daemon.logger.Debugf("(%s) CPU Shares: ~%.2f/%d, Memory: %.2f/%d MiB", svc.Name,
			stats.CPUShares, svc.CPUShares, stats.Memory, svc.Memory)
		svcHb := ServiceHeartbeat{
			Time:          time.Now().UnixNano(),
			Memory:        svc.Memory,
			CPUShares:     svc.CPUShares,
			UsedMemory:    stats.Memory,
			UsedCPUShares: stats.CPUShares,
		}

		po, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointSvcHb, svcHb)
		if err != nil {
			daemon.logger.Errorf("(%s) Failed to marshal service heartbeat: %s", svc.Name, err)
			continue
		}

		if err := bw2Iface.PublishSignal("heartbeat", po); err != nil {
			daemon.logger.Errorf("(%s) Failed to publish service heartbeat: %s", svc.Name, err)
		}
	}
	daemon.logger.Debugf("(%s) Service heartbeat publication terminated", svc.Name)

	select {
	case err := <-errChan:
		daemon.logger.Errorf("(%s) Error while profiling service: %s", svc.Name, err)
	default:
	}
}

func (daemon *SpawnpointDaemon) Decommission() error {
	bw2Iface := daemon.bw2Service.RegisterInterface("daemon", "i.spawnpoint")
	daemon.logger.Debugf("Decomissioning spawnpoint %s", daemon.Path)
	// A message without any POs is effectively a metadata de-persist
	if err := bw2Iface.PublishSignal("heartbeat"); err != nil {
		daemon.logger.Errorf("Failed to publish de-persist message: %s", err)
		return errors.Wrap(err, "Failed to publish de-persist message")
	}

	daemon.logger.Debugf("Decommissioning successful")
	return nil
}
