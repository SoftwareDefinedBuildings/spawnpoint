package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/codegangsta/cli"
	bw2 "github.com/immesys/bw2bind"
	"github.com/mgutz/ansi"
)

var BWC *bw2.BW2Client
var Us string
var PAC string

func InitCon(c *cli.Context) {
	var err error
	BWC, err = bw2.Connect(c.GlobalString("router"))
	if err != nil {
		fmt.Println("Could not connect to router: ", err)
		os.Exit(1)
	}
	Us, err = BWC.SetEntityFile(c.GlobalString("entity"))
	if err != nil {
		fmt.Println("Could not set entity: ", err)
		os.Exit(1)
	}
}
func main() {
	app := cli.NewApp()
	app.Name = "spawnctl"
	app.Usage = "Control Spawnpoints"
	app.Version = "-9001 janky-as-f***"
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

type SpawnPoint struct {
	URI      string
	LastSeen time.Time
	Alias    string
}

func (s *SpawnPoint) Good() bool {
	return time.Now().Sub(s.LastSeen) < 10*time.Second
}

func scan(baseuri string) map[string]SpawnPoint {
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
	rv := make(map[string]SpawnPoint)
	for r := range res {
		for _, po := range r.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointHeartbeat) {
				hb := struct {
					Alias string
					Time  string
				}{}
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
				v := SpawnPoint{
					URI:      uri,
					LastSeen: ls,
					Alias:    hb.Alias,
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
		fmt.Printf("[%s%s%s] seen %s%s%s ago at %s\n", ansi.ColorCode("blue+b"), res.Alias, ansi.ColorCode("reset"), color, ls, ansi.ColorCode("reset"), res.URI)
	}
	return rv
}
func actionScan(c *cli.Context) {
	InitCon(c)
	baseuri := c.String("uri")
	if len(baseuri) == 0 {
		fmt.Println("Missing URI parameter")
		os.Exit(1)
	}
	ch, err := BWC.BuildAnyChain(baseuri, "C*", Us)
	if err != nil {
		fmt.Println("Could not obtain permissions: ", err)
		os.Exit(1)
	}
	PAC = ch.Hash
	scan(baseuri)
}

func recParse(t *template.Template, filename string) *template.Template {
	t2, err := t.ParseFiles(filename)
	if err != nil {
		fmt.Println("Problem with config file: ", err)
		os.Exit(1)
	}
	return t2
}
func parseConfig(filename string) map[string]interface{} {
	tmp := template.New("root")
	tmp = recParse(tmp, filename)
	buf := &bytes.Buffer{}
	data := struct{}{}
	err := tmp.ExecuteTemplate(buf, filename, data)
	if err != nil {
		fmt.Println("Problem with config file: ", err)
		os.Exit(1)
	}
	rv := make(map[string]interface{})
	err = yaml.Unmarshal(buf.Bytes(), &rv)
	if err != nil {
		fmt.Println("Config syntax error: ", err)
		os.Exit(1)
	}
	return rv
}
func fixuri(u string) string {
	if u[len(u)-1] == '/' {
		return u[:len(u)-1]
	}
	return u
}
func actionDeploy(c *cli.Context) {
	InitCon(c)
	baseuri := fixuri(c.String("uri"))
	cfg := c.String("config")
	if len(baseuri) == 0 {
		fmt.Println("Missing 'uri' parameter")
		os.Exit(1)
	}
	if len(cfg) == 0 {
		fmt.Println("Missing 'config' parameter")
		os.Exit(1)
	}
	ch, err := BWC.BuildAnyChain(baseuri, "C*", Us)
	if err != nil {
		fmt.Println("Could not obtain permissions: ", err)
		os.Exit(1)
	}
	PAC = ch.Hash
	spawnpoints := scan(baseuri)
	//subscribe to logs
	configured := make(map[string]bool)
	x := parseConfig(cfg)
	for k, v := range x {
		castv := v.(map[interface{}]interface{})
		for _, svcvi := range castv {
			svcv := svcvi.(map[interface{}]interface{})
			entityfilename := svcv["entity"]
			entitycontents, err := ioutil.ReadFile(entityfilename.(string))
			if err != nil {
				fmt.Printf("%s[ERR] cannot find entity keyfile '%s'. Aborting.%s\n", ansi.ColorCode("red+b"), entityfilename, ansi.ColorCode("reset"))
				os.Exit(1)
			}
			svcv["entity"] = base64.StdEncoding.EncodeToString(entitycontents)
		}

		sp, ok := spawnpoints[k]
		if !ok {
			fmt.Printf("%s[ERR] config references unknown spawnpoint '%s'. Skipping this section.%s\n", ansi.ColorCode("red+b"), k, ansi.ColorCode("reset"))
			continue
		}
		if !sp.Good() {
			fmt.Printf("%s[WARN] spawnpoint '%s' appears down. Writing config anyway.%s\n", ansi.ColorCode("yellow+b"), k, ansi.ColorCode("reset"))
		}
		d, err := yaml.Marshal(&castv)
		if err != nil {
			fmt.Println("Failed to serialize config for spawnpoint", k)
			os.Exit(1)
		}
		po, _ := bw2.LoadYAMLPayloadObject(bw2.PONumSpawnpointConfig, d)
		uri := sp.URI + "ctl/cfg"
		fmt.Println("publishing cfg to: ", uri)
		err = BWC.Publish(&bw2.PublishParams{
			URI:                uri,
			PrimaryAccessChain: PAC,
			ElaboratePAC:       bw2.ElaborateFull,
			PayloadObjects:     []bw2.PayloadObject{po},
		})
		if err != nil {
			fmt.Println("ERROR publishing: ", err)
		}
		configured[k] = true
	}
	log, err := BWC.Subscribe(&bw2.SubscribeParams{
		URI:                baseuri + "/*/info/spawn/+/log",
		PrimaryAccessChain: PAC,
		ElaboratePAC:       bw2.ElaborateFull,
	})
	if err != nil {
		fmt.Println("Could not subscribe to spawnpoint svc logs: ", err)
	}
	for k, _ := range configured {
		sp := spawnpoints[k]
		uri := sp.URI + "ctl/restart"
		fmt.Println("publishing to ", uri)
		err := BWC.Publish(&bw2.PublishParams{
			URI:                uri,
			PrimaryAccessChain: PAC,
			ElaboratePAC:       bw2.ElaborateFull,
		})
		if err != nil {
			fmt.Println("Could not publish to restart: ", err)
		}
	}
	fmt.Printf("%s !! FINISHED DEPLOYMENT, TAILING LOGS. CTRL-C TO QUIT !! %s\n", ansi.ColorCode("green+b"), ansi.ColorCode("reset"))
	for msg := range log {
		for _, po := range msg.POs {
			if po.IsTypeDF(bw2.PODFSpawnpointLog) {
				entry := struct {
					Time     int64
					SPAlias  string
					Service  string
					Contents string
				}{}
				err := po.(bw2.MsgPackPayloadObject).ValueInto(&entry)
				if err != nil {
					fmt.Printf("%s[WARN] malformed log entry%s\n", ansi.ColorCode("red+b"), ansi.ColorCode("reset"))
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
