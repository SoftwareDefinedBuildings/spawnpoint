package daemon

import (
	"context"
	"encoding/gob"
	"os"
	"time"

	"github.com/pkg/errors"
)

const persistFileEnvVar = "SPAWNPOINT_PERSIST_FILE"
const defaultPersistFileName = ".manifests"

func (daemon *SpawnpointDaemon) persistSnapshots(ctx context.Context, delay time.Duration) {
	tick := time.Tick(delay)

	for {
		daemon.logger.Debug("Snapshotting running service state to file")
		persistFileName := os.Getenv(persistFileEnvVar)
		if len(persistFileName) == 0 {
			persistFileName = defaultPersistFileName
		}
		persistFile, err := os.Create(persistFileName)
		if err != nil {
			daemon.logger.Errorf("Failed to open service snapshot file: %s", err)
			return
		}

		encoder := gob.NewEncoder(persistFile)
		daemon.registryLock.RLock()
		if err := encoder.Encode(daemon.serviceRegistry); err != nil {
			daemon.logger.Errorf("Failed to encode running services: %s", err)
		}
		daemon.registryLock.RUnlock()

		if err := persistFile.Close(); err != nil {
			daemon.logger.Errorf("Failed to close service snapshot file: %s", err)
		}

		// First check if we're done
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Wait for time expiration or context cancellation
		select {
		case <-tick:
		case <-ctx.Done():
		}
	}
}

func (daemon *SpawnpointDaemon) recoverServices(ctx context.Context) error {
	daemon.logger.Debug("Attempting to recover previous services from snapshot")

	persistFileName := os.Getenv(persistFileEnvVar)
	if len(persistFileName) == 0 {
		persistFileName = defaultPersistFileName
	}
	persistFile, err := os.Open(persistFileName)
	if err != nil {
		return errors.Wrap(err, "Could not open service snapshot file")
	}
	defer persistFile.Close()

	decoder := gob.NewDecoder(persistFile)
	var registry map[string]*serviceManifest
	if err = decoder.Decode(&registry); err != nil {
		return errors.Wrap(err, "Failed to decode service snapshot")
	}
	daemon.logger.Debugf("Discovered %v services in snapshot", len(registry))

	runningServices, err := daemon.backend.ListServices(ctx)
	if err != nil {
		return errors.Wrap(err, "Failed to list running services")
	}
	servicesAsSet := sliceToSet(runningServices)

	for _, svc := range registry {
		_, ok := servicesAsSet[svc.ID]
		if ok {
			// Service kept running while spawnpoint was down
			daemon.logger.Debugf("(%s) Attempting to reassume ownership", svc.Name)
			daemon.addService(svc, false)
		} else {
			// Service went down while spawnpoint was also down
			if svc.Configuration.AutoRestart {
				daemon.logger.Debugf("(%s) Auto-restart enabled, attempting to resurrect", svc.Name)
				if err := daemon.backend.RestartService(ctx, svc.ID); err != nil {
					daemon.logger.Errorf("(%s) Failed to restart service: %s", svc.Name, err)
				} else {
					daemon.addService(svc, false)
				}
			} else {
				daemon.logger.Debugf("(%s) Auto-restart disabled, removing container", svc.Name)
				if err := daemon.backend.RemoveService(ctx, svc.ID); err != nil {
					daemon.logger.Errorf("(%s) Failed to remove old container: %s", svc.Name, err)
				}
			}
		}
	}
	return nil
}

func sliceToSet(sl []string) map[string]struct{} {
	retVal := make(map[string]struct{})
	for _, x := range sl {
		retVal[x] = struct{}{}
	}
	return retVal
}
