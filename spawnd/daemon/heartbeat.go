package daemon

import (
	"context"
	"time"

	bw2 "github.com/immesys/bw2bind"
	"github.com/pkg/errors"
)

type heartbeat struct {
	Alias           string
	Time            int64
	TotalMemory     uint32
	TotalCPU        uint32
	AvailableMemory uint32
	AvailableCPU    uint32
}

func (daemon *SpawnpointDaemon) publishHearbeats(ctx context.Context, delay time.Duration) {
	bw2Iface := daemon.bw2Service.RegisterInterface("daemon", "i.spawnpoint")
	for {
		select {
		case <-ctx.Done():
			daemon.logger.Debug("Terminating daemon heartbeat publication")
			return

		default:
			daemon.resourceLock.RLock()
			availableCPU := daemon.availableCPUShares
			availableMemory := daemon.availableMemory
			daemon.resourceLock.RUnlock()
			daemon.logger.Debug("Publishing daemon heartbeat")
			daemon.logger.Debugf("CPU: %v/%v, Memory: %v/%v", availableCPU, daemon.totalCPUShares,
				availableMemory, daemon.totalMemory)

			hb := heartbeat{
				Alias:           daemon.alias,
				Time:            time.Now().UnixNano(),
				TotalCPU:        daemon.totalCPUShares,
				TotalMemory:     daemon.totalMemory,
				AvailableCPU:    availableCPU,
				AvailableMemory: availableMemory,
			}
			hbPo, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointHeartbeat, hb)
			if err != nil {
				daemon.logger.Errorf("Failed to marshal heartbeat: %s", err)
				goto end
			}
			if err := bw2Iface.PublishSignal("heartbeat", hbPo); err != nil {
				daemon.logger.Errorf("Failed to publish daemon heartbeat: %s", err)
			}

		end:
			time.Sleep(delay)
		}
	}
}

func (daemon *SpawnpointDaemon) Decommission() error {
	bw2Iface := daemon.bw2Service.RegisterInterface("daemon", "i.spawnpoint")
	daemon.logger.Debugf("Decomissioning spawnpoint %s", daemon.alias)
	// A message without any POs is effectively a metadata de-persist
	if err := bw2Iface.PublishSignal("heartbeat"); err != nil {
		daemon.logger.Errorf("Failed to publish de-persist message: %s", err)
		return errors.Wrap(err, "Failed to publish de-persist message")
	}

	daemon.logger.Debugf("Decommissioning successful")
	return nil
}
