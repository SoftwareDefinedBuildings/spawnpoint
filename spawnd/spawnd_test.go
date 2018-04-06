package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnclient"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/daemon"
	logging "github.com/op/go-logging"
)

const spawnpointURI = "scratch.ns/spawnpoint/testing"
const bw2Entity = "testing/test.ent"
const totalCPUShares = 1024
const totalMemory = 1024

var spawnClient *spawnclient.Client

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var err error

	config := daemon.Config{
		BW2Entity:            bw2Entity,
		BW2Agent:             "172.17.0.1:28589",
		Path:                 spawnpointURI,
		CPUShares:            totalCPUShares,
		Memory:               totalMemory,
		EnableHostNetworking: false,
		EnableDeviceMapping:  false,
	}
	logging.SetBackend(logging.NewLogBackend(ioutil.Discard, "", 0))
	log := logging.MustGetLogger("spawnd-test")
	spawnpointDaemon, err := daemon.New(&config, log)
	if err != nil {
		fmt.Printf("Failed to initialize spawnpoint daemon: %s\n", err)
		os.Exit(1)
	}

	spawnClient, err = spawnclient.New("172.17.0.1:28589", "testing/test.ent")
	if err != nil {
		fmt.Printf("Failed to initialize spawnpoint client: %s\n", err)
		os.Exit(1)
	}

	wg.Add(1)
	go func() {
		spawnpointDaemon.StartLoop(ctx)
		wg.Done()
	}()

	retCode := m.Run()
	cancel()
	wg.Wait()
	os.Exit(retCode)
}

// Standard deployment of a simple service
func TestSimpleDeploy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing service logs...")
	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 1)

	t.Log("Stopping service...")
	if err := spawnClient.Stop(spawnpointURI, "demosvc"); err != nil {
		t.Fatalf("Failed to stop service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 2)
}

// Standard deployment of a simple service
func TestSimpleRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing service logs...")
	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 1)

	t.Log("Restarting service...")
	if err := spawnClient.Restart(spawnpointURI, "demosvc"); err != nil {
		t.Fatalf("Failed to restart service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 1)

	t.Log("Stopping service...")
	if err := spawnClient.Stop(spawnpointURI, "demosvc"); err != nil {
		t.Fatalf("Failed to stop service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 2)
}

// Attempt to deploy second service with same name as first
func TestDuplicateDeploy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing service logs...")
	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 1)

	t.Log("Deploying duplicate service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 409)

	t.Log("Stopping service...")
	if err := spawnClient.Stop(spawnpointURI, "demosvc"); err != nil {
		t.Fatalf("Failed to stop service: %s", err)
	}
	awaitSuccess(t, logChan, errChan, 2)
}

// Attempt to deploy service with too many CPU shares requested
func TestDeployExcessiveCPU(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares + 1,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing service logs...")
	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 503)
}

// Attempt to deploy service with too much memory requested
func TestDeployExcessiveMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory + 1,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing service logs...")
	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 503)
}

// Attempt to deploy service with too much memory and CPU requested
func TestDeployExcessiveCPUMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares + 1,
		Memory:        totalMemory + 1,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing service logs...")
	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 503)
}

// Attempt to deploy service on an oversubscribed spawnpoint
func TestDeployOversubscribed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares,
		Memory:        totalMemory,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Tailing first service logs...")
	logChan1, errChan1 := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan1:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying first service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitSuccess(t, logChan1, errChan1, 1)

	t.Log("Tailing second service logs...")
	logChan2, errChan2 := spawnClient.Tail(ctx, "demosvc2", spawnpointURI)
	select {
	case err := <-errChan2:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying second service...")
	config.Name = "demosvc2"
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan2, errChan2, 503)

	if err := spawnClient.Stop(spawnpointURI, "demosvc"); err != nil {
		t.Fatalf("Failed to stop service: %s", err)
	}
	awaitSuccess(t, logChan1, errChan1, 2)
}

// Attempt to deploy service with invalid CPU shares
func TestDeployInvalidCPU(t *testing.T) {
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     0,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err == nil {
		t.Fatalf("Expected error with invalid CPU shares: %v", config.CPUShares)
	}
}

// Attempt to deploy service with invalid memory
func TestDeployInvalidMemory(t *testing.T) {
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        0,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err == nil {
		t.Fatalf("Expected error with invalid memory allocation: %v", config.Memory)
	}
}

// Attempt to deploy service with invalid CPU and memory
func TestDeployInvalidCPUMemory(t *testing.T) {
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     0,
		Memory:        0,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err == nil {
		t.Fatalf("Expected error with invalid CPU shares %v and memory allocation %v",
			config.CPUShares, config.Memory)
	}
}

// Attempt to deploy a service with host networking, which isn't allowed
func TestDeployHostNetworking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
		UseHostNet:    true,
	}

	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 403)
}

// Attempt to deploy a service with mapped devices, which isn't allowed
func TestDeployDevices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
		Devices:       []string{"/dev/tty0"},
	}

	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 403)
}

// Attempt to deploy a service with host networking and mapped devices, which aren't allowed
func TestDeployHostNetDevices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := service.Configuration{
		Name:          "demosvc",
		Source:        "git+https://github.com/jhkolb/demosvc",
		BW2Entity:     bw2Entity,
		CPUShares:     totalCPUShares / 2,
		Memory:        totalMemory / 2,
		Build:         []string{"go get -d", "go build -o demosvc"},
		Run:           []string{"./demosvc", "200"},
		IncludedFiles: []string{"testing/params.yml"},
		AutoRestart:   false,
		UseHostNet:    true,
		Devices:       []string{"/dev/tty0"},
	}

	logChan, errChan := spawnClient.Tail(ctx, "demosvc", spawnpointURI)
	select {
	case err := <-errChan:
		t.Fatalf("Failed to tail service logs: %s", err)
	default:
	}

	t.Log("Deploying service...")
	if err := spawnClient.Deploy(&config, spawnpointURI); err != nil {
		t.Fatalf("Failed to deploy service: %s", err)
	}
	awaitFailure(t, logChan, errChan, 403)
}

func awaitSuccess(t *testing.T, logChan <-chan service.LogMessage, errChan <-chan error, successTotal int) {
	successCounter := 0
	for {
		select {
		case logMsg := <-logChan:
			if strings.HasPrefix(logMsg.Contents, "[SUCCESS]") {
				t.Log(logMsg.Contents)
				successCounter++
				if successCounter == successTotal {
					return
				}
			} else if strings.HasPrefix(logMsg.Contents, "[ERROR]") {
				t.Fatal(logMsg.Contents)
			}

		case err := <-errChan:
			t.Fatalf("Failure while tailing service logs: %s", err)
		}
	}
}

func awaitFailure(t *testing.T, logChan <-chan service.LogMessage, errChan <-chan error, code uint) {
	for {
		select {
		case logMsg := <-logChan:
			if strings.HasPrefix(logMsg.Contents, fmt.Sprintf("[ERROR %d]", code)) {
				return
			} else if strings.HasPrefix(logMsg.Contents, "[ERROR") {
				t.Fatalf("Unexpected error occurred: %s", logMsg.Contents)
			} else if strings.HasPrefix(logMsg.Contents, "[SUCCESS]") {
				t.Fatalf("Success occurred when expected failure: %s", logMsg.Contents)
			}

		case err := <-errChan:
			t.Fatalf("Unrelated error occurred while awaiting failure: %s", err)
		}
	}
}
