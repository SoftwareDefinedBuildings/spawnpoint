package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/backend"
)

func (daemon *SpawnpointDaemon) manageService(svc *serviceManifest, done chan<- struct{}) {
	var wg sync.WaitGroup
	ctx, cancelFunc := context.WithCancel(context.Background())
	restartInProgress := false

	// Cleanup functions
	defer close(done)
	defer func() {
		wg.Wait()
		if len(svc.ID) > 0 {
			daemon.logger.Debugf("(%s) Attempting to remove service", svc.Name)
			if err := daemon.backend.RemoveService(context.Background(), svc.ID); err != nil {
				daemon.logger.Errorf("(%s) Failed to remove service: %s", svc.Name, err)
				if err = daemon.publishLogMessage(svc.Name, "[ERROR 500] Failed to remove service"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
				}
				return
			}
			daemon.logger.Debugf("(%s) Service removed successfully", svc.Name)
			if err := daemon.publishLogMessage(svc.Name, "[SUCCESS] Removed service container"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
			}
		}
	}()
	defer cancelFunc()

	for event := range svc.Events {
		switch event {
		case service.Boot:
			daemon.logger.Debugf("(%s) State machine received service boot event", svc.Name)

			daemon.resourceLock.Lock()
			if svc.CPUShares <= daemon.availableCPUShares && svc.Memory <= daemon.availableMemory {
				daemon.logger.Debugf("(%s) Daemon has sufficient CPU and memory for new service", svc.Name)
				daemon.availableCPUShares -= svc.CPUShares
				daemon.availableMemory -= svc.Memory
				daemon.resourceLock.Unlock()

				defer func() {
					daemon.resourceLock.Lock()
					daemon.availableCPUShares += svc.CPUShares
					daemon.availableMemory += svc.Memory
					daemon.resourceLock.Unlock()
				}()
			} else {
				daemon.logger.Debugf("(%s) Has insufficient CPU and memory for new service, rejecting", svc.Name)
				msg := fmt.Sprintf("[ERROR 503] Insufficient resources for service. CPU: Have %v, Want %v. Mem: Have %v, Want %v",
					daemon.availableCPUShares, svc.CPUShares, daemon.availableMemory, svc.Memory)
				if err := daemon.publishLogMessage(svc.Name, msg); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
				}
				daemon.resourceLock.Unlock()
				return
			}

			daemon.logger.Debugf("(%s) Attempting to start new service", svc.Name)
			if err := daemon.publishLogMessage(svc.Name, "[INFO] Launching service..."); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
			}

			msgs := make(chan string, 20)
			go func() {
				for msg := range msgs {
					if err := daemon.publishLogMessage(svc.Name, strings.TrimSpace(msg)); err != nil {
						daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
					}
				}
			}()

			svcID, err := daemon.backend.StartService(ctx, svc.Configuration, msgs)
			if err != nil {
				daemon.logger.Errorf("(%s) Failed to start service: %s", svc.Name, err)
				if err = daemon.publishLogMessage(svc.Name, fmt.Sprintf("[ERROR 500] Failed to start service: %s", err)); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
				}
				return
			}
			daemon.logger.Debugf("(%s) Service started successfully", svc.Name)
			if err = daemon.publishLogMessage(svc.Name, "[SUCCESS] Service container has started"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
			}

			svc.ID = svcID
			daemon.registryLock.Lock()
			daemon.serviceRegistry[svc.Name] = svc
			daemon.registryLock.Unlock()
			defer func() {
				daemon.registryLock.Lock()
				delete(daemon.serviceRegistry, svc.Name)
				daemon.registryLock.Unlock()
			}()

			wg.Add(3)
			go daemon.tailLogs(ctx, svc, true, &wg)
			go daemon.monitorEvents(ctx, svc, &wg)
			go daemon.publishServiceHeartbeats(ctx, svc, heartbeatInterval, &wg)

		case service.Adopt:
			daemon.logger.Debugf("(%s) State machine received service adopt event", svc.Name)
			// Accept all previously running services without doing a quota check
			daemon.resourceLock.Lock()
			daemon.availableCPUShares -= svc.CPUShares
			daemon.availableMemory -= svc.Memory
			daemon.resourceLock.Unlock()

			defer func() {
				daemon.resourceLock.Lock()
				daemon.availableCPUShares += svc.CPUShares
				daemon.availableMemory += svc.Memory
				daemon.resourceLock.Unlock()
			}()

			daemon.registryLock.Lock()
			daemon.serviceRegistry[svc.Name] = svc
			daemon.registryLock.Unlock()
			defer func() {
				daemon.registryLock.Lock()
				delete(daemon.serviceRegistry, svc.Name)
				daemon.registryLock.Unlock()
			}()

			wg.Add(3)
			go daemon.tailLogs(ctx, svc, true, &wg)
			go daemon.monitorEvents(ctx, svc, &wg)
			go daemon.publishServiceHeartbeats(ctx, svc, heartbeatInterval, &wg)

		case service.Restart:
			daemon.logger.Debugf("(%s) State machine received service restart event", svc.Name)
			if err := daemon.backend.RestartService(ctx, svc.ID); err != nil {
				daemon.logger.Errorf("(%s) Failed to restart service: %s", svc.Name, err)
				if err = daemon.publishLogMessage(svc.Name, "[ERROR 500] Failed to restart service"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
				}
				return
			}
			if err := daemon.publishLogMessage(svc.Name, "[SUCCESS] Restarted service container"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
			}

			// Need to re-initialize container logging
			wg.Add(1)
			go daemon.tailLogs(ctx, svc, false, &wg)
			// Set flag so that inevitable service die event is ignored
			restartInProgress = true

		case service.Stop:
			daemon.logger.Debugf("(%s) State machine received service stop event", svc.Name)
			if err := daemon.backend.StopService(ctx, svc.ID); err != nil {
				daemon.logger.Errorf("(%s) Failed to stop service: %s", svc.Name, err)
				if err = daemon.publishLogMessage(svc.Name, "[ERROR 500] Failed to stop service"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
				}
				return
			}
			daemon.logger.Debugf("(%s) Service stop completed", svc.Name)
			if err := daemon.publishLogMessage(svc.Name, "[SUCCESS] Stopped service container"); err != nil {
				daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
			}
			return

		case service.Die:
			daemon.logger.Debugf("(%s) State machine received die event", svc.Name)
			if restartInProgress {
				// Service termination was part of a normal restart
				daemon.logger.Debugf("(%s) This was part of a normal restart, ignoring", svc.Name)
				restartInProgress = false
				continue
			}

			if svc.AutoRestart {
				daemon.logger.Debugf("(%s) Auto-restart enabled, attempting service restart", svc.Name)
				if err := daemon.backend.RestartService(ctx, svc.ID); err != nil {
					daemon.logger.Errorf("(%s) Failed to restart service: %s", svc.Name, err)
					if err = daemon.publishLogMessage(svc.Name, "[ERROR 500] Failed to restart service"); err != nil {
						daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
					}
					return
				}
				daemon.logger.Debugf("(%s) Service restart successful", svc.Name)
				if err := daemon.publishLogMessage(svc.Name, "[SUCCESS] Restarted service container"); err != nil {
					daemon.logger.Errorf("(%s) Failed to publish log message: %s", svc.Name, err)
				}
				// Need to re-initialize container logging
				wg.Add(1)
				go daemon.tailLogs(ctx, svc, false, &wg)
			} else {
				return
			}
		}
	}
}

func (daemon *SpawnpointDaemon) monitorEvents(ctx context.Context, svc *serviceManifest, wg *sync.WaitGroup) {
	defer wg.Done()
	eventChan, errChan := daemon.backend.MonitorService(ctx, svc.ID)
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
		daemon.logger.Errorf("(%s) Error while monitoring docker events: %s", svc.Name, err)
	default:
	}
}
