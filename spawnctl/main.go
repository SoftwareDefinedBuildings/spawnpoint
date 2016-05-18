package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"text/template"
	"time"

    "github.com/immesys/spawnpoint/objects"
	"gopkg.in/yaml.v2"

	"github.com/codegangsta/cli"
	"github.com/mgutz/ansi"
	bw2 "gopkg.in/immesys/bw2bind.v1"
)

func InitCon(c *cli.Context) (*bw2.BW2Client, string) {
	BWC, err := bw2.Connect(c.GlobalString("router"))
	if err != nil {
		fmt.Println("Could not connect to router: ", err)
		os.Exit(1)
	}

    Us, err := BWC.SetEntityFile(c.GlobalString("entity"))
	if err != nil {
		fmt.Println("Could not set entity: ", err)
		os.Exit(1)
	}

	return BWC, Us
}

func main() {
	app := cli.NewApp()
	app.Name = "spawnctl"
	app.Usage = "Control Spawnpoints"
	app.Version = "0.0.1"

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "router, r",
			Usage: "set the local router",
			Value: "127.0.0.1:28589",
		},
		cli.StringFlag{
			Name:  "entity, e",
			Usage: "set the entity keyfile",
			Value: "entity.key",
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
	}
	app.Run(os.Args)
}

func scan(baseuri string, BWC *bw2.BW2Client, PAC string) map[string]objects.SpawnPoint {
	scanuri := baseuri + "/*/info/spawn/!heartbeat"
	fmt.Println("scanning: ", scanuri)
	res, err := BWC.Query(&bw2.QueryParams{
		URI:                scanuri,
		PrimaryAccessChain: PAC,
		ElaboratePAC:       bw2.ElaborateFull,
	})
	if err != nil {
		fmt.Println("Unable to do query: ", err)
		os.Exit(1)
	}

	rv := make(map[string]objects.SpawnPoint)
	for r := range res {
		for _, po := range r.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
				hb := objects.SpawnPointHb{}
				po.(bw2.YAMLPayloadObject).ValueInto(&hb)

				uri := r.URI
				uri = uri[:len(uri)-len("info/spawn/!heartbeat")]
				fmt.Println("deduced URI as: ", uri)
				ls, err := time.Parse(time.RFC3339, hb.Time)
				if err != nil {
					fmt.Println("WARN: bad time in heartbeat from", uri)
					fmt.Println("value: ", hb.Time, "Error: ", err)
					continue
				}

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

	fmt.Println("Discovered SpawnPoints:")
	for _, res := range rv {
		dur := time.Now().Sub(res.LastSeen)
		var color string
		if dur > 20*time.Second {
			color = ansi.ColorCode("red+b")
		} else {
			color = ansi.ColorCode("green+b")
		}
		ls := res.LastSeen.Format(time.RFC822) + " (" + dur.String() + ")"
		fmt.Printf("[%s%s%s] seen %s%s%s ago at %s\n", ansi.ColorCode("blue+b"), res.Alias,
			ansi.ColorCode("reset"), color, ls, ansi.ColorCode("reset"), res.URI)
	}

	return rv
}

func actionScan(c *cli.Context) {
	BWClient, Us := InitCon(c)

	baseuri := c.String("uri")
	if len(baseuri) == 0 {
		fmt.Println("Missing URI parameter")
		os.Exit(1)
	}

	ch, err := BWClient.BuildAnyChain(baseuri, "C*", Us)
	if err != nil {
		fmt.Println("Could not obtain permissions: ", err)
		os.Exit(1)
	}
    PAC := ch.Hash
	scan(baseuri, BWClient, PAC)
}

func recParse(t *template.Template, filename string) *template.Template {
	t2, err := t.ParseFiles(filename)
	if err != nil {
		fmt.Println("Problem with config file: ", err)
		os.Exit(1)
	}
	return t2
}

func parseConfig(filename string) *map[string]objects.SvcConfig {
	tmp := template.New("root")
	tmp = recParse(tmp, filename)
	buf := bytes.Buffer{}
	data := struct{}{}
	err := tmp.ExecuteTemplate(&buf, filename, data)
	if err != nil {
		fmt.Println("Problem with config file: ", err)
		os.Exit(1)
	}

	rv := make(map[string]objects.SvcConfig)
	err = yaml.Unmarshal(buf.Bytes(), &rv)
	if err != nil {
		fmt.Println("Config syntax error: ", err)
		os.Exit(1)
	}

	return &rv
}

func fixuri(u string) string {
	if u[len(u)-1] == '/' {
		return u[:len(u)-1]
	}
	return u
}

func actionDeploy(c *cli.Context) {
	BWClient, Us := InitCon(c)

	baseuri := fixuri(c.String("uri"))
	if len(baseuri) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}

	cfg := c.String("config")
	if len(cfg) == 0 {
		fmt.Println("Missing 'config' parameter")
		os.Exit(1)
	}

	ch, err := BWClient.BuildAnyChain(baseuri, "C*", Us)
	if err != nil {
		fmt.Println("Could not obtain permissions: ", err)
		os.Exit(1)
	}
    PAC := ch.Hash

	spawnpoints := scan(baseuri, BWClient, PAC)
    logs := make([]chan *bw2.SimpleMessage, 0)

	svcs := parseConfig(cfg)
	for spName, config := range *svcs {
		entityContents, err := ioutil.ReadFile(config.Entity)
		if err != nil {
			fmt.Printf("%s[ERR] cannot find entity keyfile '%s'. Aborting.%s\n",
				ansi.ColorCode("red+b"), config.Entity, ansi.ColorCode("reset"))
			os.Exit(1)
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
			fmt.Println("Failed to serialize config for spawnpoint", spName)
			os.Exit(1)
		}

		uri := sp.URI + "ctl/cfg"
		fmt.Println("publishing cfg to: ", uri)
		err = BWClient.Publish(&bw2.PublishParams{
			URI:                uri,
			PrimaryAccessChain: PAC,
			ElaboratePAC:       bw2.ElaborateFull,
			PayloadObjects:     []bw2.PayloadObject{po},
		})
		if err != nil {
			fmt.Println("ERROR publishing: ", err)
			continue
		}

		log, err := BWClient.Subscribe(&bw2.SubscribeParams{
			URI:                sp.URI + "info/spawn/" + config.ServiceName + "/log",
			PrimaryAccessChain: PAC,
			ElaboratePAC:       bw2.ElaborateFull,
		})
		if err != nil {
			fmt.Println("ERROR subscribing to log:", err)
		} else {
			logs = append(logs, log)
		}
	}

	fmt.Printf("%s !! FINISHED DEPLOYMENT, TAILING LOGS. CTRL-C TO QUIT !! %s\n",
		ansi.ColorCode("green+b"), ansi.ColorCode("reset"))

	cases := make([]reflect.SelectCase, len(logs))
	for i, log := range logs {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(log)}
	}

	for {
		_, msgVal, _ := reflect.Select(cases)
        // Ugh, I probably have no idea what I'm doing
        msg := msgVal.(*bw2.SimpleMessage)

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
				fmt.Printf("[%s] %s%s::%s> %s%s\n",
					tstring, ansi.ColorCode("blue+b"),
					entry.SPAlias, entry.Service,
					ansi.ColorCode("reset"), strings.Trim(entry.Contents, "\n"))
			}
		}
	}
}
