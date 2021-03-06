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
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/util"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

const deploymentHistVar = "SPAWNCTL_HISTORY_FILE"
const healthHorizon = 25 * time.Second

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
					Usage: "YAML service configuration file",
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
			Name:   "deploy-last",
			Usage:  "Run the last deploy command executed from the current working directory",
			Action: actionDeployLast,
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "yes, y",
					Usage: "Skip deployment confirmation",
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
	} else {
		config.Name = svcName
	}

	var timeout time.Duration
	timeoutStr := c.String("timeout")
	if len(timeoutStr) > 0 {
		timeout, err = time.ParseDuration(timeoutStr)
		if err != nil {
			fmt.Println("Illegal timeout parameter, must be in Go's time duration format, e.g. '5s'")
			os.Exit(1)
		} else if timeout < 0 {
			fmt.Println("Timeout duration must be positive")
			os.Exit(1)
		}
	}

	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Println("Warning: Unable to get current working directory to save info for deploy-last")
	} else if err = saveDeployment(currentDir, deployment{
		BW2Entity:     entity,
		URI:           spawnpointURI,
		Name:          svcName,
		Configuration: cfgFile,
		Timeout:       timeoutStr,
	}, getDeploymentHistoryFile()); err != nil {
		fmt.Println("Warning: Failed to save deployment info for deploy-last")
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Printf("Could not create spawnpoint client: %s\n", err)
		os.Exit(1)
	}
	age, err := checkSpawnpointHealth(spawnClient, spawnpointURI)
	if err != nil {
		fmt.Printf("Failed to check spawnpoint health: %s\n", err)
		os.Exit(1)
	} else if age == 0 {
		fmt.Printf("No spawnpoint exists at %s\n", spawnpointURI)
		os.Exit(1)
	} else if age > healthHorizon {
		fmt.Printf("Spawnpoint at %s appears to be down\n", spawnpointURI)
		fmt.Printf("Last seen %s ago\n", age.String())
		os.Exit(1)
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
		os.Exit(1)
	}
	age, err := checkSpawnpointHealth(spawnClient, spawnpointURI)
	if err != nil {
		fmt.Printf("Failed to check spawnpoint health: %s\n", err)
		os.Exit(1)
	} else if age == 0 {
		fmt.Printf("No spawnpoint exists at %s\n", spawnpointURI)
		os.Exit(1)
	} else if age > healthHorizon {
		fmt.Printf("Spawnpoint at %s appears to be down\n", spawnpointURI)
		fmt.Printf("Last seen %s ago\n", age.String())
		os.Exit(1)
	}
	age, err = checkServiceHealth(spawnClient, spawnpointURI, svcName)
	if err != nil {
		fmt.Printf("Failed to check service health: %s\n", err)
		os.Exit(1)
	} else if age == 0 {
		fmt.Printf("Service %s does not exist at spawnpoint %s\n", svcName, spawnpointURI)
		os.Exit(1)
	} else if age > healthHorizon {
		fmt.Printf("Service %s is not running\n", svcName)
		fmt.Printf("Last seen %s ago\n", age.String())
		os.Exit(1)
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

	uri := c.String("uri")
	if len(uri) == 0 && len(c.Args()) > 0 {
		uri = c.Args()[0]
	}
	baseURI := fixBaseURI(uri)
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

func checkSpawnpointHealth(sc *spawnclient.Client, uri string) (time.Duration, error) {
	spawnpoints, err := sc.Scan(uri)
	if err != nil {
		return 0, errors.Wrap(err, "Failed to scan for spawnpoint at URI")
	}
	daemonHb, ok := spawnpoints[uri]
	if !ok {
		return 0, nil
	}

	daemonTimestamp := time.Unix(0, daemonHb.Time)
	duration := time.Now().Sub(daemonTimestamp)
	return duration, nil
}

func checkServiceHealth(sc *spawnclient.Client, uri string, svcName string) (time.Duration, error) {
	_, svcHbs, err := sc.Inspect(uri)
	if err != nil {
		return 0, errors.Wrap(err, "Failed to inspect spawnpoint")
	}
	svcHb, ok := svcHbs[svcName]
	if !ok {
		return 0, nil
	}
	svcTimestamp := time.Unix(0, svcHb.Time)
	duration := time.Now().Sub(svcTimestamp)
	return duration, nil
}
