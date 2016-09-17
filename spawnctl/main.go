package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/immesys/spawnpoint/objects"
	"github.com/immesys/spawnpoint/spawnclient"
	"gopkg.in/yaml.v2"

	"github.com/mgutz/ansi"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "spawnctl"
	app.Usage = "Control and Monitor Spawnpoints"
	app.Version = objects.SpawnpointVersion

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "router, r",
			Usage: "set the local router",
			Value: "",
		},
		cli.StringFlag{
			Name:   "entity, e",
			Usage:  "set the entity keyfile",
			EnvVar: "BW2_DEFAULT_ENTITY",
		},
	}

	app.Commands = []cli.Command{
		{
			Name:   "scan",
			Usage:  "Scan for running spawnpoints",
			Action: actionScan,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "a base URI to scan from",
					Value: "",
				},
			},
		},
		{
			Name:   "example",
			Usage:  "Generate an example configuration",
			Action: actionExample,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "output, o",
					Usage: "the output filename",
					Value: "example.yml",
				},
			},
		},
		{
			Name:   "deploy",
			Usage:  "Deploy a configuration to a spawnpoint",
			Action: actionDeploy,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "a base URI to deploy to",
					Value: "",
				},
				cli.StringFlag{
					Name:  "config, c",
					Usage: "a configuration to deploy",
					Value: "",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "name of the deployed service",
					Value: "",
				},
			},
		},
		{
			Name:   "tail",
			Usage:  "Tail logs of a running spawnpoint service",
			Action: actionTail,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "a base URI of spawnpoint running the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "name of the service to tail",
					Value: "",
				},
			},
		},
		{
			Name:   "restart",
			Usage:  "Restart a service running on a spawnpoint",
			Action: actionRestart,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "base URI of spawnpoint running the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "name of the service to restart",
					Value: "",
				},
			},
		},
		{
			Name:   "stop",
			Usage:  "Stop a service running on a spawnpoint",
			Action: actionStop,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "base URI of spawnpoint running the service",
					Value: "",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "name of the service to stop",
					Value: "",
				},
			},
		},
	}
	app.Run(os.Args)
}

func fixuri(u string) string {
	if len(u) > 0 && u[len(u)-1] == '/' {
		return u[:len(u)-1]
	}
	return u
}

func actionExample(c *cli.Context) error {
	f, err := os.Create("example.yml")
	if err != nil {
		fmt.Printf("could not open file: %v\n", err)
		os.Exit(1)
	}
	f.WriteString(`entity: /path/to/entity.ent
image: jhkolb/spawnpoint:amd64
source: git+http://github.com/your/repo
build: [go get -d, go build -o svcexe]
run: [./svcexe, "your", "params"]
memAlloc: 512M
cpuShares: 1024
includedFiles: [params.yml]`)
	err = f.Close()
	if err != nil {
		fmt.Printf("could not save file: %v\n", err)
		os.Exit(1)
	}
	f, err = os.Create("params.yml")
	if err != nil {
		fmt.Printf("could not open file: %v\n", err)
		os.Exit(1)
	}
	f.WriteString(`key: value
aparam: avalue`)
	err = f.Close()
	if err != nil {
		fmt.Printf("could not save file: %v\n", err)
		os.Exit(1)
	}
	return nil
}

func printLastSeen(lastSeen time.Time, name string, uri string) {
	var color string
	if !objects.IsSpawnPointGood(lastSeen) {
		color = ansi.ColorCode("red+b")
	} else {
		color = ansi.ColorCode("green+b")
	}
	dur := time.Now().Sub(lastSeen) / (10 * time.Millisecond) * (10 * time.Millisecond)
	ls := lastSeen.Format(time.RFC822) + " (" + dur.String() + ")"
	if uri != "" {
		fmt.Printf("[%s%s%s] seen %s%s%s ago at %s\n", ansi.ColorCode("blue+b"),
			name, ansi.ColorCode("reset"), color, ls, ansi.ColorCode("reset"), uri)
	} else {
		fmt.Printf("[%s%s%s] seen %s%s%s ago\n", ansi.ColorCode("blue+b"),
			name, ansi.ColorCode("reset"), color, ls, ansi.ColorCode("reset"))
	}
}

func actionScan(c *cli.Context) error {
	entityFile := c.GlobalString("entity")
	if entityFile == "" {
		fmt.Println("No Bosswave entity specified")
		os.Exit(1)
	}
	uriparam := c.String("uri")
	if uriparam == "" && len(c.Args()) > 0 {
		uriparam = c.Args()[0]
	}
	baseuri := fixuri(uriparam)
	if len(baseuri) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}
	if strings.HasSuffix(baseuri, "/") {
		baseuri += "*"
	} else if !strings.HasSuffix(baseuri, "/*") {
		baseuri += "/*"
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entityFile)
	if err != nil {
		fmt.Println("Failed to initialize spawn client:", err)
		os.Exit(1)
	}
	spawnPoints, err := spawnClient.Scan(baseuri)
	if err != nil {
		fmt.Println("Spawnpoint scan failed:", err)
		os.Exit(1)
	}

	fmt.Printf("Discovered %v SpawnPoint(s):\n", len(spawnPoints))
	// Print out status information on all discovered spawnpoints
	for _, sp := range spawnPoints {
		printLastSeen(sp.LastSeen, sp.Alias, sp.URI)
		fmt.Printf("  Available Memory: %v MB, Available Cpu Shares: %v\n",
			sp.AvailableMem, sp.AvailableCPUShares)
	}

	if len(spawnPoints) == 1 {
		// Print detailed information about single spawnpoint
		for _, sp := range spawnPoints {
			svcs, metadata, err := spawnClient.Inspect(sp.URI)
			if err != nil {
				fmt.Println("Inspect failed:", err)
				os.Exit(1)
			}

			fmt.Printf("%sMetadata:%s\n", ansi.ColorCode("blue+b"), ansi.ColorCode("reset"))
			if len(metadata) > 0 {
				for key, tuple := range metadata {
					if time.Now().Sub(time.Unix(0, tuple.Timestamp)) < objects.MetadataExpiry {
						fmt.Printf("  • %s: %s\n", key, tuple.Value)
					}
				}
			}

			fmt.Printf("%sServices:%s\n", ansi.ColorCode("blue+b"), ansi.ColorCode("reset"))
			for _, svc := range svcs {
				if time.Now().Sub(svc.LastSeen) < objects.ZombiePeriod {
					fmt.Print("  • ")
					printLastSeen(svc.LastSeen, svc.Name, "")
					fmt.Printf("      Memory: %.2f/%d MB, CPU Shares: ~%d/%d\n", svc.MemUsage, svc.MemAlloc,
						svc.CPUShareUsage, svc.CPUShares)
				}
			}
		}
	}

	return nil
}

func tailLog(log chan *objects.SPLogMsg) {
	for logMsg := range log {
		tstring := time.Unix(0, logMsg.Time).Format("01/02 15:04:05")
		prefix := fmt.Sprintf("[%s] %s%s::%s >%s ", tstring, ansi.ColorCode("blue+b"),
			logMsg.SPAlias, logMsg.Service, ansi.ColorCode("reset"))
		trimmed := strings.Trim(logMsg.Contents, "\n")
		prefixed := prefix + strings.Replace(trimmed, "\n", "\n"+prefix, -1)
		fmt.Println(prefixed)
	}
}

func actionRestart(c *cli.Context) error {
	return issueServiceCommand(c, "restart")
}

func actionStop(c *cli.Context) error {
	return issueServiceCommand(c, "stop")
}

func actionTail(c *cli.Context) error {
	return issueServiceCommand(c, "tail")
}

func issueServiceCommand(c *cli.Context, command string) error {
	entity := c.GlobalString("entity")
	if entity == "" {
		fmt.Println("Failed to specify entity file")
		os.Exit(1)
	}
	uri := c.String("uri")
	if uri == "" {
		fmt.Println("Failed to specify 'uri' parameter")
		os.Exit(1)
	}
	name := c.String("name")
	if name == "" {
		fmt.Println("Failed to specify 'name' parameter")
		os.Exit(1)
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Println("Failed to initialize spawn client:", err)
		os.Exit(1)
	}

	var log chan *objects.SPLogMsg
	switch command {
	case "restart":
		log, err = spawnClient.RestartService(uri, name)
	case "stop":
		log, err = spawnClient.StopService(uri, name)
	case "tail":
		log, err = spawnClient.TailService(uri, name)
	}
	if err != nil {
		fmt.Println("Failed to restart service:", err)
		os.Exit(1)
	}

	fmt.Printf("%sMonitoring service log. Press Ctrl-C to quit.%s\n",
		ansi.ColorCode("green+b"), ansi.ColorCode("reset"))
	tailLog(log)
	return nil
}

func parseConfig(filename string) (*objects.SvcConfig, error) {
	tmp := template.New("root")
	tmp, err := tmp.ParseFiles(filename)
	if err != nil {
		return nil, err
	}

	buf := bytes.Buffer{}
	leafName := filename[strings.LastIndex(filename, "/")+1:]
	if err = tmp.ExecuteTemplate(&buf, leafName, struct{}{}); err != nil {
		return nil, err
	}

	var rv objects.SvcConfig
	if err = yaml.Unmarshal(buf.Bytes(), &rv); err != nil {
		return nil, err
	}

	return &rv, nil
}

func actionDeploy(c *cli.Context) error {
	entity := c.GlobalString("entity")
	if entity == "" {
		fmt.Println("Missing 'entity' parameter")
		os.Exit(1)
	}
	spURI := fixuri(c.String("uri"))
	if spURI == "" {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}
	cfgFile := c.String("config")
	if cfgFile == "" {
		fmt.Println("Missing 'config' parameter")
		os.Exit(1)
	}
	svcName := c.String("name")
	if svcName == "" {
		fmt.Println("Missing 'name' parameter")
		os.Exit(1)
	}

	svcConfig, err := parseConfig(cfgFile)
	if err != nil {
		fmt.Println("Invalid service configuration file:", err)
		os.Exit(1)
	}
	svcConfig.ServiceName = svcName

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Println("Failed to initialize spawn client:", err)
		os.Exit(1)
	}
	log, err := spawnClient.DeployService(svcConfig, spURI, svcName)
	if err != nil {
		fmt.Printf("%s[ERROR]%s Service deployment failed, %v\n", ansi.ColorCode("red+b"),
			ansi.ColorCode("reset"), err)
		os.Exit(1)
	}

	fmt.Printf("%s Deployment complete, tailing log. Ctrl-C to quit.%s\n",
		ansi.ColorCode("green+b"), ansi.ColorCode("reset"))
	tailLog(log)
	return nil
}
