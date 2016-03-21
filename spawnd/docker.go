package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	docker "github.com/fsouza/go-dockerclient"
)

var DKR *docker.Client

type SpawnPointContainer struct {
	Raw         *docker.Container
	ServiceName string
}

func ConnectDocker() error {
	var err error
	DKR, err = docker.NewClient("unix:///var/run/docker.sock")
	if err != nil {
		fmt.Println("Error connecting to docker: ", err.Error())
		os.Exit(1)
	}
	ec := make(chan *docker.APIEvents)
	DKR.AddEventListener(ec)
	go func() {
		e := <-ec
		fmt.Printf("Event: %+v\n", e)
	}()
	return err
}

type ContainerLogger struct {
	output      chan SLM
	servicename string
}

func (cl *ContainerLogger) Write(p []byte) (n int, err error) {
	cl.output <- SLM{cl.servicename, string(p)}
	return len(p), nil
}

func GetSpawnedContainers() []*SpawnPointContainer {
	rawrv, err := DKR.ListContainers(docker.ListContainersOptions{
		All: true,
	})
	fmt.Println("rawrv: ", rawrv)
	if err != nil {
		panic(err)
	}
	rv := make([]*SpawnPointContainer, 0)
	for _, c := range rawrv {
		for _, n := range c.Names {
			if strings.HasPrefix(n, "/spawnpoint_") {
				container, err := DKR.InspectContainer(c.ID)
				if err != nil {
					panic(err)
				}
				rv = append(rv, &SpawnPointContainer{Raw: container, ServiceName: n[len("/spawnpoint_"):]})
				break
			}
		}
	}
	return rv
}

/*
type Manifest struct {
  ServiceName string
	Entity      []byte
	Container   string
	AptRequires string
	Build       []string
	Run         []string
	Params			map[string]string
}
*/

func RestartContainer(cfg *Manifest, log chan SLM) (*SpawnPointContainer, error) {
	log <- SLM{cfg.ServiceName, "restarting container"}
	curContainers := GetSpawnedContainers()
	for _, c := range curContainers {
		fmt.Println("names: ", c.ServiceName, cfg.ServiceName)
		if c.ServiceName == cfg.ServiceName {
			log <- SLM{cfg.ServiceName, "Found existing container for service, deleting"}
			if c.Raw.State.Running {
				err := DKR.StopContainer(c.Raw.ID, 3)
				if err != nil {
					log <- SLM{cfg.ServiceName, "COULD NOT KILL CONTAINER: " + err.Error()}
				}
			}
			err := DKR.RemoveContainer(docker.RemoveContainerOptions{
				ID:            c.Raw.ID,
				RemoveVolumes: true,
				Force:         true,
			})
			if err != nil {
				log <- SLM{cfg.ServiceName, "Error removing container: " + err.Error()}
				return nil, err
			}
		}
	}
	imgname := "spawnpoint_image_" + cfg.ServiceName
	err := DKR.RemoveImage(imgname)
	if err != nil && err != docker.ErrNoSuchImage {
		log <- SLM{cfg.ServiceName, "Error removing image" + err.Error()}
	}
	clog := ContainerLogger{log, cfg.ServiceName}
	/*
		rrt0 := strings.Split(cfg.Container, "/")
		if len(rrt0) != 2 {
			log <- "Bad container parameter"
			return nil, errors.New("Bad container")
		}
		rrt1 := strings.Split(rrt0[0], ":")
		if len(rrt1) != 2 {
			log <- "Bad container parameter"
			return nil, errors.New("Bad container")
		}*/
	/*
		err := DKR.PullImage(docker.PullImageOptions{
			Repository:   "immesys/spawnpoint",
			Tag:          "amd64",
			OutputStream: &clog,
		}, docker.AuthConfiguration{})
		if err != nil {
			log <- "Error pulling container: " + err.Error()
			return nil, err
		}
	*/
	dfile := []string{"FROM immesys/spawnpoint:amd64"}
	dfile = append(dfile, cfg.Build...)
	tdir, err := ioutil.TempDir("", "spdkr")
	tname := tdir + "/Dockerfile"
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile(tname, []byte(strings.Join(dfile, "\n")), 0666)
	if err != nil {
		panic(err)
	}
	err = DKR.BuildImage(docker.BuildImageOptions{
		Dockerfile:   "Dockerfile",
		OutputStream: &clog,
		ContextDir:   tdir,
		//Pull:         true,
		Name:    imgname,
		NoCache: true,
	})
	if err != nil {
		log <- SLM{cfg.ServiceName, "Building container error: " + err.Error()}
		return nil, err
	}
	cnt, err := DKR.CreateContainer(docker.CreateContainerOptions{
		Name: "spawnpoint_" + cfg.ServiceName,
		Config: &docker.Config{
			WorkingDir:   "/srv/spawnpoint/",
			OnBuild:      cfg.Build,
			Cmd:          cfg.Run,
			Image:        imgname,
			AttachStdout: true,
			AttachStderr: true,
			AttachStdin:  true,
            Memory:       int64(cfg.MemAlloc) * 1024 * 1024,
            CPUShares:    int64(cfg.CpuShares),
		},
		HostConfig: &docker.HostConfig{
			NetworkMode: "host",
		},
	})
	if err != nil {
		log <- SLM{cfg.ServiceName, "Could not create container: " + err.Error()}
		return nil, err
	}
	err = DKR.StartContainer(cnt.ID, &docker.HostConfig{
		NetworkMode: "host",
	})
	if err != nil {
		log <- SLM{cfg.ServiceName, "Could not start container: " + err.Error()}
		return nil, err
	}
	//Attach
	go DKR.AttachToContainer(docker.AttachToContainerOptions{
		Container:    cnt.ID,
		OutputStream: &clog,
		ErrorStream:  &clog,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})
	/*
		err = DKR.AttachToContainer(docker.AttachToContainerOptions{
			Container:    cnt.ID,
			OutputStream: &clog,
			ErrorStream:  &clog,
			Stdout:       true,
			Stderr:       true,
		})
		if err != nil {
			log <- SLM{cfg.ServiceName, "Could not attach to container: " + err.Error()}
			return nil, err
		}*/
	return &SpawnPointContainer{Raw: cnt, ServiceName: cfg.ServiceName}, nil
}
