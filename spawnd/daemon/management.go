package daemon

import (
	"context"
	"fmt"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/backend"
)

func (daemon *SpawnpointDaemon) manageService(svcConfig *service.Configuration, events chan service.Event, done chan<- struct{}) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	restartInProgress := false
	var svc *runningService
	defer close(done)
	defer cancelFunc()

	for event := range events {
		switch event {
		case service.Boot:
			daemon.logger.Debugf("(%s) State machine received service boot event", svcConfig.Name)

			daemon.resourceLock.Lock()
			if svcConfig.CPUShares <= daemon.availableCPUShares && svcConfig.Memory <= daemon.availableMemory {
				daemon.logger.Debugf("(%s) Daemon has sufficient CPU and memory for new service", svcConfig.Name)
				daemon.availableCPUShares -= svcConfig.CPUShares
				daemon.availableMemory -= svcConfig.Memory
				daemon.resourceLock.Unlock()

				defer func() {
					daemon.resourceLock.Lock()
					daemon.availableCPUShares += svcConfig.CPUShares
					daemon.availableMemory += svcConfig.Memory
					daemon.resourceLock.Unlock()
				}()
			} else {
				daemon.logger.Debugf("(%s) Has insufficient CPU and memory for new service, rejecting", svcConfig.Name)
				msg := fmt.Sprintf("[ERROR] Insufficient resources for service. CPU: Have %v, Want %v. Mem: Have %v, Want %v",
					daemon.availableCPUShares, svcConfig.CPUShares, daemon.availableMemory, svcConfig.Memory)
				if err := daemon.publishLogMessage(svcConfig.Name, msg); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
				}
				daemon.resourceLock.Unlock()
				return
			}

			daemon.logger.Debugf("(%s) Attempting to start new service", svcConfig.Name)
			svcID, err := daemon.backend.StartService(ctx, svcConfig)
			if err != nil {
				daemon.logger.Errorf("(%s) Failed to start service: %s", svcConfig.Name, err)
				if err = daemon.publishLogMessage(svcConfig.Name, fmt.Sprintf("[ERROR] Failed to start service: %s", err)); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
				}
				return
			}
			daemon.logger.Debugf("(%s) Service started successfully", svcConfig.Name)
			if err = daemon.publishLogMessage(svcConfig.Name, "[SUCCESS] Service container has started"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
			}

			svc = &runningService{
				Configuration: svcConfig,
				ID:            svcID,
				Events:        events,
			}
			daemon.registryLock.Lock()
			daemon.serviceRegistry[svcConfig.Name] = svc
			daemon.registryLock.Unlock()
			defer func() {
				daemon.registryLock.Lock()
				delete(daemon.serviceRegistry, svcConfig.Name)
				daemon.registryLock.Unlock()
			}()

			go daemon.tailLogs(ctx, svc, true)
			go daemon.monitorEvents(ctx, svc)

		case service.Restart:
			daemon.logger.Debugf("(%s) State machine received service restart event", svcConfig.Name)
			if svc == nil {
				daemon.logger.Criticalf("(%s) Encountered nil service manifest", svcConfig.Name)
				return
			}
			if err := daemon.backend.RestartService(ctx, svc.ID); err != nil {
				daemon.logger.Errorf("(%s) Failed to restart service: %s", svc.Name, err)
				if err = daemon.publishLogMessage(svcConfig.Name, "[ERROR] Failed to restart service"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
				}
				return
			}
			if err := daemon.publishLogMessage(svcConfig.Name, "[SUCCESS] Restarted service container"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
			}

			// Need to re-initialize container logging
			go daemon.tailLogs(ctx, svc, false)
			// Set flag so that inevitable service die event is ignored
			restartInProgress = true

		case service.Stop:
			daemon.logger.Debugf("(%s) State machine received service stop event", svcConfig.Name)
			if svc == nil {
				daemon.logger.Criticalf("(%s) Encountered nil service manifest", svcConfig.Name)
				return
			}
			if err := daemon.backend.StopService(ctx, svc.ID); err != nil {
				daemon.logger.Errorf("(%s) Failed to stop service: %s", svcConfig.Name, err)
				if err = daemon.publishLogMessage(svcConfig.Name, "[ERROR] Failed to stop service"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
				}
				return
			}
			if err := daemon.publishLogMessage(svcConfig.Name, "[SUCCESS] Stopped service container"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
			}

			if err := daemon.backend.RemoveService(ctx, svc.ID); err != nil {
				daemon.logger.Errorf("(%s) Failed to remove service: %s", svcConfig.Name, err)
				if err = daemon.publishLogMessage(svcConfig.Name, "[ERROR] Failed to remove service"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
				}
				return
			}
			if err := daemon.publishLogMessage(svcConfig.Name, "[SUCCESS] Removed service container"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
			}
			return

		case service.Die:
			daemon.logger.Debugf("(%s) State machine received die event", svcConfig.Name)
			if restartInProgress {
				// Service termination was part of a normal restart
				daemon.logger.Debugf("(%s) This was part of a normal restart, ignoring", svcConfig.Name)
				restartInProgress = false
				continue
			}
			if svc == nil {
				daemon.logger.Criticalf("(%s) Encountered nil service manifest", svcConfig.Name)
				return
			}

			if svc.AutoRestart {
				daemon.logger.Debugf("(%s) Auto-restart enabled, attempting service restart", svcConfig.Name)
				if err := daemon.backend.RestartService(ctx, svc.ID); err != nil {
					daemon.logger.Errorf("(%s) Failed to restart service: %s", svc.Name, err)
					if err = daemon.publishLogMessage(svcConfig.Name, "[ERROR] Failed to restart service"); err != nil {
						daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
					}
					return
				}
				if err := daemon.publishLogMessage(svcConfig.Name, "[SUCCESS] Restarted service container"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svcConfig.Name, err)
				}
				// Need to re-initialize container logging
				go daemon.tailLogs(ctx, svc, false)
			} else {
				if err := daemon.backend.RemoveService(ctx, svc.ID); err != nil {
					daemon.logger.Errorf("(%s) Failed to remove service: %s", svcConfig.Name, err)
				}
				return
			}
		}
	}
}

func (daemon *SpawnpointDaemon) monitorEvents(ctx context.Context, svc *runningService) {
	eventChan, errChan := daemon.backend.MonitorService(context.Background(), svc.ID)
	for event := range eventChan {
		switch event {
		case backend.Die:
			daemon.logger.Debugf("(%s) Container has died", svc.Name)
			svc.Events <- service.Die

		default:
			daemon.logger.Warningf("(%s) Unknown event received for container", svc.Name)
		}
	}
	select {
	case err := <-errChan:
		fmt.Printf("(%s) Error while monitoring docker events: %s", svc.Name, err)
	default:
	}
}
