package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	docker "github.com/fsouza/go-dockerclient"
)

const TIMEOUT_LEN = 3

var DKR *docker.Client

type SpawnPointContainer struct {
	Raw         *docker.Container
	ServiceName string
}

func ConnectDocker() (chan *docker.APIEvents, error) {
	var err error
	DKR, err = docker.NewClient("unix:///var/run/docker.sock")
	if err != nil {
		return nil, err
	}

	ev_chan := make(chan *docker.APIEvents)
	DKR.AddEventListener(ev_chan)
	return ev_chan, err
}

func GetSpawnedContainers() ([]*SpawnPointContainer, error) {
	rawrv, err := DKR.ListContainers(docker.ListContainersOptions{
		All: true,
	})
	if err != nil {
		return nil, err
	}

	rv := make([]*SpawnPointContainer, 0)
	for _, containerInfo := range rawrv {
		for _, name := range containerInfo.Names {
			if strings.HasPrefix(name, "/spawnpoint_") {
				container, err := DKR.InspectContainer(containerInfo.ID)
				if err != nil {
					return nil, err
				} else {
					rv = append(rv, &SpawnPointContainer{
						Raw:         container,
						ServiceName: name[len("/spawnpoint_"):],
					})
					break
				}
			}
		}
	}

	return rv, nil
}

func StopContainer(serviceName string, remove bool) error {
	curContainers, err := GetSpawnedContainers()
	if err != nil {
		fmt.Println("Failed to get spawned container list")
		return err
	}

	for _, containerInfo := range curContainers {
		if containerInfo.ServiceName == serviceName {
			fmt.Println("Found existing container for service, deleting")
			if containerInfo.Raw.State.Running {
				err := DKR.StopContainer(containerInfo.Raw.ID, TIMEOUT_LEN)
				if err != nil {
					fmt.Println("Failed to kill running container for svc", serviceName)
					fmt.Println(err)
					return err
				}
			}

			if remove {
				fmt.Println("Removing existing container for svc", serviceName)
				err := DKR.RemoveContainer(docker.RemoveContainerOptions{
					ID:            containerInfo.Raw.ID,
					RemoveVolumes: true,
					Force:         false,
				})
				if err != nil {
					fmt.Printf("Failed to remove container for", serviceName)
					return err
				}
			}
		}
	}

	return nil
}

func RestartContainer(cfg *Manifest, rebuildImage bool) (*SpawnPointContainer, error) {
	// First remove previous container
	fmt.Println("Restarting container for svc", cfg.ServiceName)
	err := StopContainer(cfg.ServiceName, rebuildImage)
	if err != nil {
		fmt.Println("Failed to remove container for svc", cfg.ServiceName)
		return nil, err
	}

	// Then rebuild image if necessary
	imgname := "spawnpoint_image_" + cfg.ServiceName
	if rebuildImage {
		err = DKR.RemoveImage(imgname)
		if err != nil && err != docker.ErrNoSuchImage {
			fmt.Println("Failed to remove image for svc", cfg.ServiceName)
			return nil, err
		}

		dfile := []string{"FROM immesys/spawnpoint:amd64"}
		dfile = append(dfile, cfg.Build...)

		tdir, err := ioutil.TempDir("", "spawnpoint")
		tname := tdir + "/dockerfile"
		if err != nil {
			return nil, err
		}
		err = ioutil.WriteFile(tname, []byte(strings.Join(dfile, "\n")), 0666)
		if err != nil {
			fmt.Println("Failed to construct dockerfile for", cfg.ServiceName)
			return nil, err
		}

		err = DKR.BuildImage(docker.BuildImageOptions{
			Dockerfile:   "dockerfile",
			OutputStream: os.Stdout,
			ContextDir:   tdir,
			Name:         imgname,
			NoCache:      true,
		})
		if err != nil {
			fmt.Println("Error building new container for svc", cfg.ServiceName)
			fmt.Println(err)
			return nil, err
		}
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
		},
		HostConfig: &docker.HostConfig{
			NetworkMode: "host",
		},
	})
	if err != nil {
		fmt.Println("Error building new container for svc", cfg.ServiceName)
		fmt.Println(err)
		return nil, err
	}

	err = DKR.StartContainer(cnt.ID, &docker.HostConfig{
		NetworkMode: "host",
        Memory:      int64(cfg.MemAlloc) * 1024 * 1024,
        CPUShares:   int64(cfg.CpuShares),
	})
	if err != nil {
		fmt.Println("Failed to start container for svc", cfg.ServiceName)
		return nil, err
	}

	// Attach
	go DKR.AttachToContainer(docker.AttachToContainerOptions{
		Container:    cnt.ID,
		OutputStream: os.Stdout,
		ErrorStream:  os.Stderr,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})

	return &SpawnPointContainer{Raw: cnt, ServiceName: cfg.ServiceName}, nil
}
