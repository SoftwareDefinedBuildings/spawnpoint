package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v2"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnclient"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/daemon"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/util"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "spawnctl"
	app.Usage = "Interact with Spawnpoint daemons"
	app.Version = util.VersionNum

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "router, r",
			Usage:  "Set the BW2 agent to use",
			EnvVar: "BW2_AGENT",
		},
		cli.StringFlag{
			Name:   "entity, e",
			Usage:  "Set the BW2 entity to use",
			EnvVar: "BW2_DEFAULT_ENTITY",
		},
	}

	app.Commands = []cli.Command{
		{
			Name:   "deploy",
			Usage:  "Deploy a configuration to a Spawnpoint daemon",
			Action: actionDeploy,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "uri, u",
					Usage:  "BW2 URI of the destination Spawnpoint",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.StringFlag{
					Name:  "configuration, c",
					Usage: "The configuration to deploy",
					Value: "",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "Name of the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "timeout, t",
					Usage: "Timeout duration (optional)",
					Value: "",
				},
			},
		},
		{
			Name:   "restart",
			Usage:  "Restart a running service",
			Action: actionRestart,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "uri, u",
					Usage:  "BW2 URI of the host Spawnpoint",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "Name of the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "timeout, t",
					Usage: "Timeout duration (optional)",
					Value: "",
				},
			},
		},
		{
			Name:   "stop",
			Usage:  "Stop a running service",
			Action: actionStop,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "uri, u",
					Usage:  "BW2 URI of the host Spawnpoint",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "Name of the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "timeout, t",
					Usage: "Timeout duration (optional)",
					Value: "",
				},
			},
		},
		{
			Name:   "tail",
			Usage:  "Tail logs of a running service",
			Action: actionTail,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "uri, u",
					Usage:  "BW2 URI of the host Spawnpoint",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "Name of the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "timeout, t",
					Usage: "Timeout duration (optional)",
					Value: "",
				},
			},
		},
		{
			Name:   "scan",
			Usage:  "Scan a base URI for running Spawnpoints",
			Action: actionScan,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "Base BW2 URI to scan",
					Value: "",
				},
			},
		},
	}

	app.Run(os.Args)
}

func actionDeploy(c *cli.Context) error {
	entity := c.GlobalString("entity")
	if len(entity) == 0 {
		fmt.Println("Missing 'entity' parameter")
		os.Exit(1)
	}

	spawnpointURI := fixURI(c.String("uri"))
	if len(spawnpointURI) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}

	cfgFile := c.String("configuration")
	if len(cfgFile) == 0 {
		fmt.Println("Missing 'configuration' parameter")
		os.Exit(1)
	}
	config, err := parseSvcConfig(cfgFile)
	if err != nil {
		fmt.Printf("Failed to parse service configuration file: %s\n", err)
		os.Exit(1)
	}

	svcName := c.String("name")
	if len(svcName) == 0 {
		svcName = config.Name
		if len(svcName) == 0 {
			fmt.Println("Missing 'name' parameter or 'Name' field in service configuration")
			os.Exit(1)
		}
	}

	var timeout time.Duration
	timeoutStr := c.String("timeout")
	if len(timeoutStr) > 0 {
		timeout, err = time.ParseDuration(timeoutStr)
		if err != nil {
			fmt.Println("Illegal timeout parameter")
			os.Exit(1)
		} else if timeout < 0 {
			fmt.Println("Timeout duration must be positive")
			os.Exit(1)
		}
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Printf("Could not create spawnpoint client: %s\n", err)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if timeout == 0 {
		ctx = context.Background()
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
	}
	logChan, errChan := spawnClient.Tail(ctx, svcName, spawnpointURI)

	// Check if an error has already occurred
	select {
	case err = <-errChan:
		fmt.Printf("Could not tail service logs: %s\n", err)
		os.Exit(1)
	default:
	}

	if err = spawnClient.Deploy(config, spawnpointURI); err != nil {
		fmt.Printf("Failed to deploy service: %s\n", err)
		os.Exit(1)
	}

	if timeout == 0 {
		fmt.Println("Tailing service logs. Press CTRL-c to exit...")
	} else {
		fmt.Printf("Tailing service logs for %s. Press CTRL-c to exit early...\n", timeout.String())
	}
	for msg := range logChan {
		fmt.Println(strings.TrimSpace(msg.Contents))
	}
	// Check again if any errors occurred while tailing service log
	select {
	case err = <-errChan:
		fmt.Printf("Error occurred while tailing logs: %s\n", err)
		os.Exit(1)
	default:
	}

	return nil
}

func actionRestart(c *cli.Context) error {
	return manipulateService(c, "restart")
}

func actionStop(c *cli.Context) error {
	return manipulateService(c, "stop")
}

func actionTail(c *cli.Context) error {
	return manipulateService(c, "tail")
}

func manipulateService(c *cli.Context, command string) error {
	entity := c.GlobalString("entity")
	if len(entity) == 0 {
		fmt.Println("Missing 'entity' parameter")
		os.Exit(1)
	}
	spawnpointURI := fixURI(c.String("uri"))
	if len(spawnpointURI) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}
	svcName := c.String("name")
	if len(svcName) == 0 {
		fmt.Println("Missing 'name' parameter")
		os.Exit(1)
	}

	var timeout time.Duration
	var err error
	timeoutStr := c.String("timeout")
	if len(timeoutStr) > 0 {
		timeout, err = time.ParseDuration(timeoutStr)
		if err != nil {
			fmt.Println("Illegal timeout parameter")
			os.Exit(1)
		} else if timeout < 0 {
			fmt.Println("Timeout duration must be positive")
			os.Exit(1)
		}
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Printf("Could not create spawnpoint client: %s\n", err)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if timeout == 0 {
		ctx = context.Background()
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
	}
	logChan, errChan := spawnClient.Tail(ctx, svcName, spawnpointURI)

	// Check if an error has already occurred
	select {
	case err = <-errChan:
		fmt.Printf("Could not tail service logs: %s\n", err)
		os.Exit(1)
	default:
	}

	switch command {
	case "stop":
		if err = spawnClient.Stop(spawnpointURI, svcName); err != nil {
			fmt.Printf("Could not stop service: %s\n", err)
			os.Exit(1)
		}
	case "restart":
		if err = spawnClient.Restart(spawnpointURI, svcName); err != nil {
			fmt.Printf("Could not restart service: %s\n", err)
			os.Exit(1)
		}
	case "tail":

	default:
		fmt.Printf("Error: unknown service manipulation operation %s\n", command)
		os.Exit(1)
	}

	if timeout == 0 {
		fmt.Println("Tailing service logs. Press CTRL-c to exit...")
	} else {
		fmt.Printf("Tailing service logs for %s. Press CTRL-c to exit early...\n", timeout.String())
	}
	for msg := range logChan {
		fmt.Println(strings.TrimSpace(msg.Contents))
	}
	// Check again if any errors occurred while tailing service log
	select {
	case err = <-errChan:
		fmt.Printf("Error occurred while tailing logs: %s\n", err)
		os.Exit(1)
	default:
	}

	return nil
}

func actionScan(c *cli.Context) error {
	entity := c.GlobalString("entity")
	if len(entity) == 0 {
		fmt.Println("Missing 'entity' parameter")
		os.Exit(1)
	}
	baseURI := fixBaseURI(c.String("uri"))
	if len(baseURI) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Printf("Could not create spawnpoint client: %s\n", err)
		os.Exit(1)
	}

	heartbeats, err := spawnClient.Scan(baseURI)
	if err != nil {
		fmt.Printf("Scan failed: %s\n", err)
		os.Exit(1)
	}

	if len(heartbeats) == 1 {
		// Guaranteed to iterate once
		for uri := range heartbeats {
			daemonHb, svcHbs, err := spawnClient.Inspect(uri)
			if err != nil {
				fmt.Printf("Inspect failed: %s\n", err)
				os.Exit(1)
			}
			printSpawnpointDetails(uri, daemonHb, svcHbs)
		}
	} else {
		fmt.Printf("Discovered %v Spawnpoint(s)\n", len(heartbeats))
		for uri, hb := range heartbeats {
			printSpawnpointStatus(uri, &hb)
		}
	}

	return nil
}

func fixURI(uri string) string {
	if len(uri) > 0 && uri[len(uri)-1] == '/' {
		return uri[:len(uri)-1]
	}
	return uri
}

func fixBaseURI(uri string) string {
	if len(uri) == 0 {
		return uri
	} else if strings.HasSuffix(uri, "/*") {
		return uri
	} else if strings.HasSuffix(uri, "/") {
		return uri + "*"
	}
	return uri + "/*"
}

func parseSvcConfig(configFile string) (*service.Configuration, error) {
	contents, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read configuration file")
	}

	var svcConfig service.Configuration
	if err = yaml.Unmarshal(contents, &svcConfig); err != nil {
		return nil, errors.Wrap(err, "Failed to parse service configuration")
	}

	return &svcConfig, nil
}

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
