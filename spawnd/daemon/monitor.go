package daemon

import (
	"context"
	"time"

	cpu "github.com/shirou/gopsutil/cpu"
	mem "github.com/shirou/gopsutil/mem"
)

func (daemon *SpawnpointDaemon) monitorHostResources(ctx context.Context, delay time.Duration) {
	tick := time.Tick(delay)
	totalCPUs, err := cpu.Counts(false)
	if err != nil {
		daemon.logger.Errorf("Failed to obtain host CPU count: %s", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			daemon.logger.Debug("Terminating resource monitoring")
			return

		case <-tick:
			percentages, err := cpu.PercentWithContext(ctx, delay, false)
			if err != nil {
				daemon.logger.Errorf("Failed to obtain CPU consumption: %s", err)
				continue
			}
			consumedCPUs := (percentages[0] / 100.0) * float64(totalCPUs)

			memStatus, err := mem.VirtualMemoryWithContext(ctx)
			if err != nil {
				daemon.logger.Errorf("Failed to obtain memory stats: %s", err)
				continue
			}

			var advertisedCPUShares uint64
			var advertisedMem uint64
			daemon.resourceLock.RLock()
			advertisedCPUShares = daemon.availableCPUShares
			advertisedMem = daemon.availableMemory
			daemon.resourceLock.RUnlock()

			if (float64(totalCPUs)-consumedCPUs)*1024 < float64(advertisedCPUShares) {
				daemon.logger.Warningf("Host CPU appears overloaded: %.2f/%d cores in use while advertising %d shares",
					consumedCPUs, totalCPUs, advertisedCPUShares)
			} else {
				daemon.logger.Debugf("%.2f/%d host CPU cores in use. Advertising %d shares", consumedCPUs,
					totalCPUs, advertisedCPUShares)
			}

			availableMem := memStatus.Available / (1024.0 * 1024.0) // Convert bytes to MiB
			if memStatus.Available/(1024.0*1024.0) < advertisedMem {
				daemon.logger.Warningf("Host memory appears overloaded: %.2f MiB available while advertising %d MiB",
					availableMem, advertisedMem)
			} else {
				daemon.logger.Debugf("%d MiB of memory available, advertising %d MiB", availableMem, advertisedMem)
			}
		}
	}
}
