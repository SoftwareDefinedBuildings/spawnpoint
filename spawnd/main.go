package main

import (
	"context"
	"io/ioutil"
	"os"

	yaml "gopkg.in/yaml.v2"

	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/daemon"
	"github.com/SoftwareDefinedBuildings/spawnpoint/spawnd/util"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "spawnd"
	app.Usage = "Perform spawnpoint daemon operations"
	app.Version = util.VersionNum

	app.Commands = []cli.Command{
		{
			Name:   "run",
			Usage:  "Run the spawnpoint daemon",
			Action: actionRun,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "config, c",
					Usage: "Specify a configuration file for the daemon",
					Value: "config.yml",
				},
			},
		},
		{
			Name:   "decommission",
			Usage:  "Decomission a spawnpoint daemon",
			Action: actionDecommission,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "config, c",
					Usage: "Specify a configuration file for the daemon",
					Value: "config.yml",
				},
			},
		},
	}

	app.Run(os.Args)
}

func actionRun(c *cli.Context) error {
	log := util.InitLogger("spawnd")

	var config daemon.Config
	configFile := c.String("config")
	configContents, err := ioutil.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Failed to read configuration file %s: %s", configFile, err)
	}
	if err = yaml.Unmarshal(configContents, &config); err != nil {
		log.Fatalf("Failed to parse configuration YAML: %s", err)
	}

	spawnpointDaemon, err := daemon.New(&config, log)
	if err != nil {
		log.Fatalf("Failed to initialize spawnd: %s", err)
	}
	spawnpointDaemon.StartLoop(context.Background())
	return nil
}

func actionDecommission(c *cli.Context) error {
	log := util.InitLogger("spawnd")
	var config daemon.Config
	configFile := c.String("config")
	configContents, err := ioutil.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Failed to read configuration file %s: %s", configFile, err)
	}
	if err = yaml.Unmarshal(configContents, &config); err != nil {
		log.Fatalf("Failed to parse configuration YAML: %s", err)
	}

	spawnpointDaemon, err := daemon.New(&config, log)
	if err != nil {
		log.Fatalf("Failed to initialize spawnd: %s", err)
	}
	return spawnpointDaemon.Decommission()
}
