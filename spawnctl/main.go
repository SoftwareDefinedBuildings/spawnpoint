package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"io/ioutil"
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

const versionNum = `0.3.6`

type prevDeployment struct {
	URI        string
	ConfigFile string
	Name       string
	Entity     string
}

func main() {
	app := cli.NewApp()
	app.Name = "spawnctl"
	app.Usage = "Control and Monitor Spawnpoints"
	app.Version = versionNum

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
					Name:   "uri, u",
					Usage:  "a base URI to scan from",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.BoolFlag{
					Name:  "all, a",
					Usage: "display all containers, even stopped containers, for a spawnpoint",
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
					Name:   "uri, u",
					Usage:  "a base URI to deploy to",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
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
			Name:   "deploy-last",
			Usage:  "Run the last deploy command executed in the current directory",
			Action: actionDeployLast,
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "yes,y",
					Usage: "skip deployment confirmation",
				},
				cli.StringFlag{
					Name:   "file, f",
					EnvVar: "SPAWNPOINT_HISTORY_FILE",
				},
			},
		},
		{
			Name:   "tail",
			Usage:  "Tail logs of a running spawnpoint service",
			Action: actionTail,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "uri, u",
					Usage:  "a base URI of spawnpoint running the service",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
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
					Name:   "uri, u",
					Usage:  "base URI of spawnpoint running the service",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
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
					Name:   "uri, u",
					Usage:  "base URI of spawnpoint running the service",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "name of the service to stop",
					Value: "",
				},
			},
		},
		{
			Name:   "inspect",
			Usage:  "Inspect a specific service running on a spawnpoint",
			Action: actionInspect,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:   "uri, u",
					Usage:  "base URI of the spawnpoint running the service",
					Value:  "",
					EnvVar: "SPAWNPOINT_DEFAULT_URI",
				},
				cli.StringFlag{
					Name:  "name, n",
					Usage: "name of the service to inspect",
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
					if time.Now().Sub(time.Unix(0, tuple.Timestamp)) < objects.MetadataCutoff {
						fmt.Printf("  • %s: %s\n", key, tuple.Value)
					}
				}
			}

			fmt.Printf("%sServices:%s\n", ansi.ColorCode("blue+b"), ansi.ColorCode("reset"))
			for _, svc := range svcs {
				if (objects.IsSpawnPointGood(svc.LastSeen) || c.Bool("all")) &&
					time.Now().Sub(svc.LastSeen) < objects.ZombiePeriod {
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

func actionInspect(c *cli.Context) error {
	entityFile := c.GlobalString("entity")
	if entityFile == "" {
		fmt.Println("No Bosswave entity specified")
		os.Exit(1)
	}
	uriparam := c.String("uri")
	baseuri := fixuri(uriparam)
	if len(baseuri) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}

	nameParam := c.String("name")
	if nameParam == "" {
		fmt.Println("Missing 'name' parameter")
		os.Exit(1)
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entityFile)
	if err != nil {
		fmt.Println("Failed to initialize spawn client:", err)
		os.Exit(1)
	}
	svcInfo, err := spawnClient.InspectService(uriparam, nameParam)
	if err != nil {
		fmt.Println("Spawnpoint scan failed:", err)
		os.Exit(1)
	}

	printLastSeen(svcInfo.LastSeen, svcInfo.Name, svcInfo.HostURI)
	fmt.Printf("CPU Usage: %v/%v Shares\n", svcInfo.CPUShareUsage, svcInfo.CPUShares)
	fmt.Printf("Memory Usage: %v/%v MiB\n", svcInfo.MemUsage, svcInfo.MemAlloc)
	fmt.Printf("Original Configuration File:\n")
	fmt.Printf(indentText(svcInfo.OriginalConfig, 1))

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

	spawnpoints, err := spawnClient.Scan(uri)
	if err != nil {
		fmt.Println("Failed to scan for spawnpoint:", err)
	}
	spAlias := uri[strings.LastIndex(uri, "/")+1:]
	sp, ok := spawnpoints[spAlias]
	if !ok {
		fmt.Printf("Error: spawnpoint at %s does not exist\n", uri)
		os.Exit(1)
	} else if !sp.Good() {
		fmt.Printf("Error: spawnpoint at %s appears down\n", uri)
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

	deployment := prevDeployment{
		URI:        spURI,
		ConfigFile: cfgFile,
		Name:       svcName,
		Entity:     entity,
	}
	if err := saveDeployment(deployment); err != nil {
		fmt.Println("Warning: Unable to save to spawnpoint history file:", err)
	}

	svcConfig, err := parseConfig(cfgFile)
	if err != nil {
		fmt.Println("Invalid service configuration file:", err)
		os.Exit(1)
	}
	svcConfig.ServiceName = svcName

	rawConfig, err := ioutil.ReadFile(cfgFile)
	if err != nil {
		fmt.Println("Failed to read service configuration file:", err)
		os.Exit(1)
	}

	spawnClient, err := spawnclient.New(c.GlobalString("router"), entity)
	if err != nil {
		fmt.Println("Failed to initialize spawn client:", err)
		os.Exit(1)
	}
	log, err := spawnClient.DeployService(string(rawConfig), svcConfig, spURI, svcName)
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

func getDeploymentHistoryFile() string {
	historyFile := os.Getenv("SPAWNPOINT_HISTORY_FILE")
	if historyFile == "" {
		historyFile = os.Getenv("HOME") + "/.spawnpoint_history"
	}
	return historyFile
}

func readPrevDeploymentFile(fileName string) (*map[string]prevDeployment, error) {
	var oldDeployments map[string]prevDeployment
	fileReader, err := os.Open(fileName)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		oldDeployments = make(map[string]prevDeployment)
		return &oldDeployments, nil
	}
	defer fileReader.Close()

	decoder := gob.NewDecoder(fileReader)
	if err = decoder.Decode(&oldDeployments); err != nil {
		return nil, err
	}
	return &oldDeployments, nil
}

func saveDeployment(dep prevDeployment) error {
	oldDeployments, err := readPrevDeploymentFile(getDeploymentHistoryFile())
	if err != nil {
		return err
	}

	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}
	(*oldDeployments)[currentDir] = dep

	fileWriter, err := os.Create(getDeploymentHistoryFile())
	if err != nil {
		return err
	}
	defer fileWriter.Close()
	encoder := gob.NewEncoder(fileWriter)
	if err = encoder.Encode(*oldDeployments); err != nil {
		return err
	}
	return nil
}

func actionDeployLast(c *cli.Context) {
	sourceFile := c.String("file")
	if sourceFile == "" {
		sourceFile = getDeploymentHistoryFile()
	}

	oldDeployments, err := readPrevDeploymentFile(sourceFile)
	if err != nil {
		fmt.Printf("Failed to read deployment history file: %v\n", err)
		os.Exit(1)
	}

	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Printf("Failed to get current working directory: %v\n", err)
		os.Exit(1)
	}

	prevDep, ok := (*oldDeployments)[currentDir]
	if !ok {
		fmt.Println("No previous deployment for this directory")
		os.Exit(1)
	}
	command := fmt.Sprintf("spawnctl -e %s deploy -u %s -c %s -n %s", prevDep.Entity,
		prevDep.URI, prevDep.ConfigFile, prevDep.Name)

	proceed := c.Bool("yes")
	if !proceed {
		fmt.Println("This will run:")
		fmt.Printf("\t%s%s%s\n", ansi.ColorCode("green"), command, ansi.ColorCode("reset"))
		fmt.Println("Proceed? [Y/n]")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		proceed = (input == "y\n" || input == "Y\n" || input == "\n")
	}

	if proceed {
		flags := flag.NewFlagSet("spawnctl", 0)
		flags.String("uri", "", "")
		flags.String("config", "", "")
		flags.String("name", "", "")
		c.GlobalSet("entity", prevDep.Entity)
		ctxt := cli.NewContext(c.App, flags, c)
		ctxt.Set("uri", prevDep.URI)
		ctxt.Set("config", prevDep.ConfigFile)
		ctxt.Set("name", prevDep.Name)
		actionDeploy(ctxt)
	}
}

func indentText(s string, indentLevel int) string {
	lines := strings.Split(s, "\n")
	prefix := ""
	for i := 0; i < indentLevel; i++ {
		prefix = "    " + prefix
	}
	for i := 0; i < len(lines); i++ {
		if len(strings.TrimSpace(lines[i])) > 0 {
			lines[i] = prefix + lines[i]
		}
	}

	return strings.Join(lines, "\n")
}
