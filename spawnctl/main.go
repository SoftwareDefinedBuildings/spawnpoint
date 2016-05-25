package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"text/template"
	"time"

	"github.com/immesys/spawnpoint/objects"
	"github.com/immesys/spawnpoint/uris"
	"gopkg.in/yaml.v2"

	"github.com/codegangsta/cli"
	"github.com/mgutz/ansi"
	bw2 "gopkg.in/immesys/bw2bind.v5"
)

func InitCon(c *cli.Context) *bw2.BW2Client {
	BWC, err := bw2.Connect(c.GlobalString("router"))
	if err != nil {
		fmt.Println("Could not connect to router: ", err)
		os.Exit(1)
	}

	_, err = BWC.SetEntityFile(c.GlobalString("entity"))
	if err != nil {
		fmt.Println("Could not set entity: ", err)
		os.Exit(1)
	}

	BWC.OverrideAutoChainTo(true)
	return BWC
}

func main() {
	app := cli.NewApp()
	app.Name = "spawnctl"
	app.Usage = "Control and Monitor Spawnpoints"
	app.Version = "0.0.1"

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "router, r",
			Usage: "set the local router",
			Value: "127.0.0.1:28589",
		},
		cli.StringFlag{
			Name:   "entity, e",
			Usage:  "set the entity keyfile",
			Value:  "entity.key",
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
			},
		},
		{
			Name:   "monitor",
			Usage:  "Monitor a running spawnpoint",
			Action: actionMonitor,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "uri, u",
					Usage: "a base URI to monitor",
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

func scan(baseuri string, BWC *bw2.BW2Client) map[string]objects.SpawnPoint {
	scanuri := uris.SignalPath(baseuri, "heartbeat")
	res, err := BWC.Query(&bw2.QueryParams{URI: scanuri})
	if err != nil {
		fmt.Println("Unable to do query: ", err)
		os.Exit(1)
	}

	rv := make(map[string]objects.SpawnPoint)
	for r := range res {
		for _, po := range r.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
				hb := objects.SpawnPointHb{}
				err = po.(bw2.MsgPackPayloadObject).ValueInto(&hb)
				if err != nil {
					fmt.Println("Received malformed heartbeat:", err)
				}

				uri := r.URI
				uri = uri[:len(uri)-len("s.spawnpoint/server/i.spawnpoint/signal/heartbeat")]
				ls := time.Unix(0, hb.Time)

				v := objects.SpawnPoint{
					URI:                uri,
					LastSeen:           ls,
					Alias:              hb.Alias,
					AvailableMem:       hb.AvailableMem,
					AvailableCpuShares: hb.AvailableCpuShares,
				}
				rv[hb.Alias] = v
			}
		}
	}

	return rv
}

func actionScan(c *cli.Context) error {
	BWClient := InitCon(c)

	baseuri := fixuri(c.String("uri"))
	if len(baseuri) == 0 {
		msg := "Missing URI parameter"
		fmt.Println(msg)
		return errors.New(msg)
	}

	if strings.HasSuffix(baseuri, "/") {
		baseuri += "+"
	} else if !strings.HasSuffix(baseuri, "/+") {
		baseuri += "/+"
	}

	spawnPoints := scan(baseuri, BWClient)
	fmt.Println("Discovered SpawnPoints:")
	for _, sp := range spawnPoints {
		var color string
		if !sp.Good() {
			color = ansi.ColorCode("red+b")
		} else {
			color = ansi.ColorCode("green+b")
		}
		dur := time.Now().Sub(sp.LastSeen)
		ls := sp.LastSeen.Format(time.RFC822) + " (" + dur.String() + ")"
		fmt.Printf("[%s%s%s] seen %s%s%s ago at %s\n", ansi.ColorCode("blue+b"), sp.Alias,
			ansi.ColorCode("reset"), color, ls, ansi.ColorCode("reset"), sp.URI)
	}

	return nil
}

func recParse(t *template.Template, filename string) *template.Template {
	t2, err := t.ParseFiles(filename)
	if err != nil {
		fmt.Println("Problem with config file: ", err)
		os.Exit(1)
	}
	return t2
}

func parseConfig(filename string) *map[string](map[string]objects.SvcConfig) {
	tmp := template.New("root")
	tmp = recParse(tmp, filename)
	buf := bytes.Buffer{}
	data := struct{}{}
	err := tmp.ExecuteTemplate(&buf, filename, data)
	if err != nil {
		fmt.Println("Problem with config file: ", err)
		os.Exit(1)
	}

	rv := make(map[string](map[string]objects.SvcConfig))
	err = yaml.Unmarshal(buf.Bytes(), &rv)
	if err != nil {
		fmt.Println("Config syntax error: ", err)
		os.Exit(1)
	}

	return &rv
}

func fixuri(u string) string {
	if len(u) > 0 && u[len(u)-1] == '/' {
		return u[:len(u)-1]
	}
	return u
}

func taillogs(logs []chan *bw2.SimpleMessage) {
	cases := make([]reflect.SelectCase, len(logs))
	for i, log := range logs {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(log)}
	}

	for {
		_, msgVal, _ := reflect.Select(cases)
		// I may have no idea what I'm doing
		msg := msgVal.Interface().(*bw2.SimpleMessage)

		for _, po := range msg.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointLog) {
				entry := objects.SPLog{}
				err := po.(bw2.MsgPackPayloadObject).ValueInto(&entry)
				if err != nil {
					fmt.Printf("%s[WARN] malformed log entry%s\n", ansi.ColorCode("red+b"),
						ansi.ColorCode("reset"))
					continue
				}

				tstring := time.Unix(0, entry.Time).Format("01/02 15:04:05")
				fmt.Printf("[%s] %s%s::%s > %s%s\n",
					tstring, ansi.ColorCode("blue+b"),
					entry.SPAlias, entry.Service,
					ansi.ColorCode("reset"), strings.Trim(entry.Contents, "\n"))
			}
		}
	}
}

func actionDeploy(c *cli.Context) error {
	BWClient := InitCon(c)

	baseuri := fixuri(c.String("uri"))
	if len(baseuri) == 0 {
		msg := "Missing 'uri' parameter"
		fmt.Println(msg)
		return errors.New(msg)
	}

	if strings.HasSuffix(baseuri, "/") {
		baseuri += "*"
	} else if !strings.HasSuffix(baseuri, "/*") {
		baseuri += "/*"
	}

	cfg := c.String("config")
	if len(cfg) == 0 {
		msg := "Missing 'config' parameter"
		fmt.Println(msg)
		return errors.New(msg)
	}

	spawnpoints := scan(baseuri, BWClient)
	logs := make([]chan *bw2.SimpleMessage, 0)

	spDeployments := parseConfig(cfg)
	for spName, svcs := range *spDeployments {
		for svcName, config := range svcs {
			config.ServiceName = svcName
			entityContents, err := ioutil.ReadFile(config.Entity)
			if err != nil {
				msg := fmt.Sprintf("Cannot find entity keyfile '%s'. Aborting", config.Entity)
				fmt.Println(msg)
				return errors.New(msg)
			}
			config.Entity = base64.StdEncoding.EncodeToString(entityContents)

			sp, ok := spawnpoints[spName]
			if !ok {
				fmt.Printf("%s[ERR] config references unknown spawnpoint '%s'. Skipping this section.%s\n",
					ansi.ColorCode("red+b"), spName, ansi.ColorCode("reset"))
				continue
			}
			if !sp.Good() {
				fmt.Printf("%s[WARN] spawnpoint '%s' appears down. Writing config anyway.%s\n",
					ansi.ColorCode("yellow+b"), spName, ansi.ColorCode("reset"))
			}

			po, err := bw2.CreateYAMLPayloadObject(bw2.PONumSpawnpointConfig, config)
			if err != nil {
				msg := "Failed to serialize config for spawnpoint" + spName
				fmt.Println(msg)
				return errors.New(msg)
			}

			spawnpointURI := fixuri(sp.URI)
			logUri := uris.ServiceSignalPath(spawnpointURI, svcName, "log")
			log, err := BWClient.Subscribe(&bw2.SubscribeParams{
				URI: logUri,
			})
			if err != nil {
				fmt.Println("ERROR subscribing to log:", err)
			} else {
				fmt.Println("Subscribed to log:", logUri)
				logs = append(logs, log)
			}

			uri := uris.SlotPath(spawnpointURI, "config")
			fmt.Println("publishing cfg to: ", uri)
			err = BWClient.Publish(&bw2.PublishParams{
				URI:            uri,
				PayloadObjects: []bw2.PayloadObject{po},
			})
			if err != nil {
				msg := fmt.Sprintf("Failed to publish config: %v", err)
				fmt.Println(msg)
				return errors.New(msg)
			}
		}
	}

	fmt.Printf("%s !! FINISHED DEPLOYMENT, TAILING LOGS. CTRL-C TO QUIT !! %s\n",
		ansi.ColorCode("green+b"), ansi.ColorCode("reset"))
	taillogs(logs)

	return nil
}

func actionMonitor(c *cli.Context) error {
	baseuri := fixuri(c.String("uri"))
	if baseuri == "" {
		msg := "Missing 'uri' parameter"
		fmt.Println(msg)
		return errors.New(msg)
	}

	uri := uris.ServiceSignalPath(baseuri, "+", "log")

	BWClient := InitCon(c)

	log, err := BWClient.Subscribe(&bw2.SubscribeParams{URI: uri})
	if err != nil {
		msg := fmt.Sprintf("Could not subscribe to log URI: %v", err)
		fmt.Println(msg)
		return errors.New(msg)
	}

	fmt.Printf("%sMonitoring log URI %s. Ctrl-C to quit%s\n", ansi.ColorCode("green+b"),
		uri, ansi.ColorCode("reset"))
	taillogs([]chan *bw2.SimpleMessage{log})

	return nil
}

func actionRestart(c *cli.Context) error {
	return manipulateService(c, "restart")
}

func actionStop(c *cli.Context) error {
	return manipulateService(c, "stop")
}

func manipulateService(c *cli.Context, command string) error {
	baseuri := fixuri(c.String("uri"))
	if baseuri == "" {
		msg := "Missing 'uri' parameter"
		fmt.Println(msg)
		return errors.New(msg)
	}

	svcName := c.String("name")
	if svcName == "" {
		msg := "Missing 'name' parameter"
		fmt.Println(msg)
		return errors.New(msg)
	}

	BWClient := InitCon(c)

	subUri := uris.ServiceSignalPath(baseuri, svcName, "log")
	log, err := BWClient.Subscribe(&bw2.SubscribeParams{URI: subUri})
	if err != nil {
		msg := fmt.Sprintf("Could not subscribe to log URI: %v", err)
		fmt.Println(msg)
		return errors.New(msg)
	}

	po := bw2.CreateTextPayloadObject(bw2.PONumString, svcName)
	err = BWClient.Publish(&bw2.PublishParams{
		URI:            uris.SlotPath(baseuri, command),
		PayloadObjects: []bw2.PayloadObject{po},
	})
	if err != nil {
		fmt.Printf("Failed to publish %s request: %v\n", command, err)
		return err
	}

	fmt.Printf("%sMonitoring log URI %s. Ctrl-C to quit%s\n", ansi.ColorCode("green+b"),
		subUri, ansi.ColorCode("reset"))
	taillogs([]chan *bw2.SimpleMessage{log})

	return nil
}
