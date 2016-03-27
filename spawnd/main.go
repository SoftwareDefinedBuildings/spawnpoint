package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	bw2 "gopkg.in/immesys/bw2bind.v1"
	yaml "gopkg.in/yaml.v2"
)

type Config struct {
	Entity      string
	Alias       string
	Path        string
	LocalRouter string
	MemAlloc    string
	CpuShares   uint64
}

type SpawnPointHb struct {
	Alias              string
	Time               string
	AvailableMem       uint64
	AvailableCpuShares uint64
}

var BWC *bw2.BW2Client
var PAC string
var Us string
var Cfg Config
var olog chan SLM
var totalMem uint64       // Memory dedicated to Spawnpoint, in MiB
var totalCpuShares uint64 // CPU shares for Spawnpoint, 1024 per core
var availableMem uint64
var availableCpuShares uint64
var eventCh chan *docker.APIEvents

var availLock sync.Mutex

type SLM struct {
	Service string
	Message string
}

const SpawnPointConfigType = "67.0.2.0"
const SpawnPointHeartbeatType = "67.0.2.1"

func main() {
	Cfg = Config{}
	cfgContents, err := ioutil.ReadFile("config.yml")
	if err != nil {
		fmt.Println("Could not read config: ", err)
		os.Exit(1)
	}
	err = yaml.Unmarshal(cfgContents, &Cfg)
	if err != nil {
		fmt.Println("Config file error: ", err)
		os.Exit(1)
	}

	totalCpuShares = Cfg.CpuShares
	availableCpuShares = totalCpuShares

	rawMem := Cfg.MemAlloc
	memAlloc, err := parseMemAlloc(rawMem)
	if err != nil {
		fmt.Println("Invalid Spawnpoint memory allocation: ", err)
		os.Exit(1)
	}
	totalMem = memAlloc
	availableMem = totalMem

	curconfig = make(map[string]Manifest)

	if Cfg.LocalRouter == "" {
		Cfg.LocalRouter = "127.0.0.1:28589"
	}
	BWC, err = bw2.Connect(Cfg.LocalRouter)
	if err != nil {
		fmt.Println("Could not connect to local router: ", err)
		os.Exit(1)
	}

	Us, err = BWC.SetEntityFile(Cfg.Entity)
	if err != nil {
		fmt.Println("Could not set entity: ", err)
		os.Exit(1)
	}
	pac, err := BWC.BuildAnyChain(path("*"), "PC", Us)
	if err != nil {
		fmt.Println("Could not obtain permissions: ", err)
		os.Exit(1)
	}
	PAC = pac.Hash
	fmt.Println("Connected to router and obtained permissions")

	//Start docker connection
	eventCh, err = ConnectDocker()
	if err != nil {
		fmt.Println("Could not connect to Docker")
		os.Exit(1)
	}
	go monitorDockerEvents(&eventCh)

	newconfigs, err := BWC.Subscribe(&bw2.SubscribeParams{
		URI:                path("ctl/cfg"),
		PrimaryAccessChain: PAC,
		ElaboratePAC:       bw2.ElaborateFull,
	})
	if err != nil {
		fmt.Println("Could not subscribe to config URI: ", err)
		os.Exit(1)
	}
	restart, err := BWC.Subscribe(&bw2.SubscribeParams{
		URI:                path("ctl/restart"),
		PrimaryAccessChain: PAC,
		ElaboratePAC:       bw2.ElaborateFull,
	})
	if err != nil {
		fmt.Println("Could not subscribe to restart URI: ", err)
		os.Exit(1)
	}

	olog = make(chan SLM, 100)
	go doOlog()
	go heartbeat()
	fmt.Println("spawnpoint active")

	for {
		select {
		case ncfg := <-newconfigs:
			ncfg.Dump()
			handleConfig(ncfg)
		case r := <-restart:
			r.Dump()
			svcName := ""
			for _, po := range r.POs {
				if po.IsTypeDF(bw2.PODFString) {
					svcName = string(po.GetContents())
				}
			}

			if svcName != "" {
				restartService(svcName)
			} else {
				respawn()
			}
		}
	}
}

type SPLog struct {
	Time     int64
	SPAlias  string
	Service  string
	Contents string
}

func PubLog(svcname string, POs []bw2.PayloadObject) {
	err := BWC.Publish(&bw2.PublishParams{
		URI:                path("info/spawn/" + svcname + "/log"),
		PrimaryAccessChain: PAC,
		ElaboratePAC:       bw2.ElaborateFull,
		Persist:            true,
		PayloadObjects:     POs,
	})
	if err != nil {
		fmt.Println("Error publishing log: ", err)
	}
}
func doOlog() {
	for {
		POs := make(map[string][]bw2.PayloadObject)
		msg1 := <-olog
		po1, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog,
			SPLog{
				time.Now().UnixNano(),
				Cfg.Alias,
				msg1.Service,
				msg1.Message,
			})
		if err != nil {
			panic(err)
		}
		POs[msg1.Service] = []bw2.PayloadObject{po1}
		//Opportunistically add another few messages if they are in the channel
	outer:
		for i := 0; i < 80; i++ {
			select {
			case msgN := <-olog:
				poN, err := bw2.CreateMsgPackPayloadObject(bw2.PONumSpawnpointLog,
					SPLog{
						time.Now().UnixNano(),
						Cfg.Alias,
						msgN.Service,
						msgN.Message,
					})
				if err != nil {
					panic(err)
				}
				exslice, ok := POs[msgN.Service]
				if ok {
					POs[msgN.Service] = append(exslice, poN)
				} else {
					POs[msgN.Service] = []bw2.PayloadObject{poN}
				}
			default:
				break outer
			}
		}
		for svc, poslice := range POs {
			PubLog(svc, poslice)
		}
	}
}

func heartbeat() {
	for {
		availLock.Lock()
		mem := availableMem
		shares := availableCpuShares
		availLock.Unlock()

		msg := SpawnPointHb{
			Cfg.Alias,
			time.Now().Format(time.RFC3339),
			mem,
			shares,
		}
		po, err := bw2.CreateYAMLPayloadObject(bw2.FromDotForm(SpawnPointHeartbeatType), msg)
		if err != nil {
			panic(err)
		}
		hburi := path("info/spawn/!heartbeat")
		err = BWC.Publish(&bw2.PublishParams{
			URI:                hburi,
			PrimaryAccessChain: PAC,
			ElaboratePAC:       bw2.ElaborateFull,
			Persist:            true,
			PayloadObjects:     []bw2.PayloadObject{po},
		})
		if err != nil {
			fmt.Println("[ERROR] failed to publish heartbeat: ", err)
		}
		time.Sleep(5 * time.Second)
	}
}
func path(suffix string) string {
	return Cfg.Path + "/" + suffix
}

var curconfig map[string]Manifest
var configLock sync.Mutex

func respawn() {
	olog <- SLM{"meta", "doing respawn"}
	configLock.Lock()
	for _, manifest := range curconfig {
		cnt, err := RestartContainer(&manifest, olog)
		if err != nil {
			fmt.Println("ERROR IN CONTAINER: ", err)
		} else {
			fmt.Println("Container started ok")
			manifest.Container = cnt
		}
	}
	configLock.Unlock()
}

func restartService(serviceName string) {
	olog <- SLM{serviceName, "attempting restart"}
	configLock.Lock()
	for name, manifest := range curconfig {
		if name == serviceName {
			cnt, err := RestartContainer(&manifest, olog)
			if err != nil {
				fmt.Println("ERROR IN CONTAINER: ", err)
			} else {
				fmt.Println("Container started ok")
				manifest.Container = cnt
			}
		}
	}
	configLock.Unlock()
}

func handleConfig(m *bw2.SimpleMessage) {
	defer func() {
		r := recover()
		if r != nil {
			olog <- SLM{Service: "meta", Message: "Error in configuration file : " + fmt.Sprintf("%+v", r)}
		}
	}()
	cfg, ok := m.GetOnePODF(bw2.PODFSpawnpointConfig).(bw2.YAMLPayloadObject)
	if !ok {
		return
	}
	toplevel := make(map[string]interface{})
	err := cfg.ValueInto(toplevel)
	if err != nil {
		panic(err)
	}
	for svcname, val := range toplevel {
		svc := val.(map[interface{}]interface{})
		rawMem, ok := svc["memAlloc"].(string)
		if !ok {
			panic(errors.New("bad memAlloc option"))
		}
		memAlloc, err := parseMemAlloc(rawMem)
		if err != nil {
			panic(err)
		}

		rawCpuShares, ok := svc["cpuShares"].(string)
		if !ok {
			panic(errors.New("bad cpuShares option"))
		}
		cpuShares, err := strconv.ParseUint(rawCpuShares, 0, 64)
		if err != nil {
			panic(err)
		}

		econtents, err := base64.StdEncoding.DecodeString(svc["entity"].(string))
		if err != nil {
			panic(err)
		}
		rawbuildcontents := strings.Split(svc["build"].(string), "\n")
		buildcontents := make([]string, len(rawbuildcontents)+5)
		sourceparts := strings.SplitN(svc["source"].(string), "+", 2)
		switch sourceparts[0] {
		case "git":
			buildcontents[0] = "RUN git clone " + sourceparts[1] + " /srv/target"
		default:
			fmt.Println("Unknown source type")
			continue
		}
		parmsstring, err := yaml.Marshal(svc["params"])
		if err != nil {
			panic(err)
		}
		parms64 := base64.StdEncoding.EncodeToString(parmsstring)

		buildcontents[1] = "WORKDIR /srv/target"
		buildcontents[2] = "RUN echo " + svc["entity"].(string) + " | base64 --decode > entity.key"
		if svc["aptrequires"] != nil && svc["aptrequires"].(string) != "" {
			buildcontents[3] = "RUN apt-get update && apt-get install -y " + svc["aptrequires"].(string)
		} else {
			buildcontents[3] = "RUN echo 'no apt-requires'"
		}
		buildcontents[4] = "RUN echo " + parms64 + " | base64 --decode > params.yml"
		for idx, b := range rawbuildcontents {
			buildcontents[idx+5] = "RUN " + b
		}
		run := make([]string, len(svc["run"].([]interface{})))
		for idx, val := range svc["run"].([]interface{}) {
			run[idx] = val.(string)
		}

		// Check if Spawnpoint has sufficient resources. If not, reject configuration
		availLock.Lock()
		defer availLock.Unlock()
		if memAlloc > availableMem {
			err = fmt.Errorf("Insufficient Spawnpoint memory for requested allocation (have %d, want %d)", availableMem, memAlloc)
			panic(err)
		} else if cpuShares > availableCpuShares {
			err = fmt.Errorf("Insufficient Spawnpoint CPU shares for requested allocation (have %d, want %d)", availableCpuShares, cpuShares)
			panic(err)
		} else {
			availableMem -= memAlloc
			availableCpuShares -= cpuShares
		}

		mf := Manifest{
			ServiceName:   svcname,
			Entity:        econtents,
			ContainerType: svc["container"].(string),
			MemAlloc:      memAlloc,
			CpuShares:     cpuShares,
			Build:         buildcontents,
			Run:           run,
			AutoRestart:   svc["autoRestart"].(bool),
		}
		configLock.Lock()
		curconfig[svcname] = mf
		configLock.Unlock()
	}
}

/*
demosvc:
	entity: demokey.key
	container: spawnpoint-default
	aptrequires: fortune-mod
	source: git+http://github.com/immesys/demosvc
	build:
		go build
	run:
		demosvc
	params:
		message: "hello world"
		to: {{$base}}/funtimes/info/dsvc/out
*/
type Manifest struct {
	ServiceName   string
	Entity        []byte
	ContainerType string
	MemAlloc      uint64
	CpuShares     uint64
	//Automatically add the source stuff to the front of Build
	Build       []string
	Run         []string
	AutoRestart bool
	Container   *SpawnPointContainer
}

func parseMemAlloc(alloc string) (uint64, error) {
	if alloc == "" {
		return 0, errors.New("No memory allocation in config")
	}
	suffix := alloc[len(alloc)-1:]
	memAlloc, err := strconv.ParseUint(alloc[:len(alloc)-1], 0, 64)
	if err != nil {
		return 0, err
	}

	if suffix == "G" || suffix == "g" {
		memAlloc *= 1024
	} else if suffix != "M" && suffix != "m" {
		err = errors.New("Memory allocation amount must be in units of M or G")
		return 0, err
	}

	return memAlloc, nil
}

func monitorDockerEvents(ec *chan *docker.APIEvents) {
	for event := range *ec {
		if event.Action == "die" {
			configLock.Lock()
			for name, manifest := range curconfig {
				if manifest.Container.Raw.ID == event.Actor.ID {
					olog <- SLM{name, "Container stopped"}
					if manifest.AutoRestart {
						cnt, err := RestartContainer(&manifest, olog)
						if err != nil {
							fmt.Println("ERROR IN CONTAINER: ", err)
							delete(curconfig, name)
							availLock.Lock()
							availableCpuShares += manifest.CpuShares
							availableMem += manifest.MemAlloc
							availLock.Unlock()
						} else {
							fmt.Println("Container restart ok")
							manifest.Container = cnt
						}
					}
				}
			}
			configLock.Unlock()
		}
	}
}
