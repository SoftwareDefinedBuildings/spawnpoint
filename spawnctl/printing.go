package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/daemon"
)

func printSpawnpointStatus(uri string, hb *daemon.Heartbeat) {
	tokens := strings.Split(uri, "/")
	alias := tokens[len(tokens)-1]
	lastSeen := time.Unix(0, hb.Time)
	duration := time.Now().Sub(lastSeen) / (10 * time.Millisecond) * (10 * time.Millisecond)

	fmt.Printf("[%s] seen %s (%s) ago at %s\n", alias, lastSeen.Format(time.RFC822), duration.String(), uri)
	fmt.Printf("Available CPU Shares: %v/%v\n", hb.AvailableCPU, hb.TotalCPU)
	fmt.Printf("Available Memory: %v/%v\n", hb.AvailableMemory, hb.TotalMemory)

	fmt.Printf("%v Running Service(s)\n", len(hb.Services))
	for _, service := range hb.Services {
		fmt.Printf("  • %s\n", service)
	}
}

func printSpawnpointDetails(uri string, daemonHb *daemon.Heartbeat, svcHbs map[string]daemon.ServiceHeartbeat) {
	tokens := strings.Split(uri, "/")
	alias := tokens[len(tokens)-1]
	lastSeen := time.Unix(0, daemonHb.Time)
	duration := time.Now().Sub(lastSeen) / (10 * time.Millisecond) * (10 * time.Millisecond)

	fmt.Printf("[%s] seen %s (%s) ago at %s\n", alias, lastSeen.Format(time.RFC822), duration.String(), uri)
	fmt.Printf("Available CPU Shares: %v/%v\n", daemonHb.AvailableCPU, daemonHb.TotalCPU)
	fmt.Printf("Available Memory: %v/%v\n", daemonHb.AvailableMemory, daemonHb.TotalMemory)

	fmt.Printf("%v Running Service(s)\n", len(daemonHb.Services))
	for name, svcHb := range svcHbs {
		lastSeen := time.Unix(0, svcHb.Time)
		duration := time.Now().Sub(lastSeen) / (10 * time.Millisecond) * (10 * time.Millisecond)
		fmt.Printf("• [%s] seen %s (%s) ago.\n", name, lastSeen.Format(time.RFC822), duration.String())
		fmt.Printf("  CPU: ~%.2f/%d Shares. Memory: %.2f/%d MiB\n", svcHb.UsedCPUShares, svcHb.CPUShares,
			svcHb.UsedMemory, svcHb.Memory)
	}
}
