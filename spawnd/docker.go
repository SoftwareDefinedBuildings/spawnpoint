package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	docker "github.com/fsouza/go-dockerclient"
)

const timeoutLen = 3

var dkr *docker.Client

type SpawnPointContainer struct {
	Raw         *docker.Container
	ServiceName string
	StatChan    *chan *docker.Stats
}

func ConnectDocker() (chan *docker.APIEvents, error) {
	var err error
	dkr, err = docker.NewClient("unix:///var/run/docker.sock")
	if err != nil {
		return nil, err
	}

	evChan := make(chan *docker.APIEvents)
	dkr.AddEventListener(evChan)
	return evChan, err
}

func GetSpawnedContainers() ([]*SpawnPointContainer, error) {
	rawrv, err := dkr.ListContainers(docker.ListContainersOptions{
		All: true,
	})
	if err != nil {
		return nil, err
	}

	var rv []*SpawnPointContainer
	for _, containerInfo := range rawrv {
		for _, name := range containerInfo.Names {
			if strings.HasPrefix(name, "/spawnpoint_") {
				container, err := dkr.InspectContainer(containerInfo.ID)
				if err != nil {
					return nil, err
				}

				rv = append(rv, &SpawnPointContainer{
					Raw:         container,
					ServiceName: name[len("/spawnpoint_"):],
				})
				break
			}
		}
	}

	return rv, nil
}

func StopContainer(serviceName string) error {
	curContainers, err := GetSpawnedContainers()
	if err != nil {
		return err
	}

	for _, containerInfo := range curContainers {
		if containerInfo.ServiceName == serviceName {
			fmt.Println("Found existing container for service, deleting")
			if containerInfo.Raw.State.Running {
				err := dkr.StopContainer(containerInfo.Raw.ID, timeoutLen)
				if err != nil {
					return err
				}
			}

			fmt.Println("Removing existing container for svc", serviceName)
			err := dkr.RemoveContainer(docker.RemoveContainerOptions{
				ID:            containerInfo.Raw.ID,
				RemoveVolumes: true,
				Force:         false,
			})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func RestartContainer(cfg *Manifest, bwRouter string, rebuildImg bool) (*SpawnPointContainer, error) {
	// First remove previous container
	fmt.Println("Restarting container for svc", cfg.ServiceName)
	err := StopContainer(cfg.ServiceName)
	if err != nil {
		fmt.Println("Failed to remove container for svc", cfg.ServiceName)
		return nil, err
	}

	// Then rebuild image if necessary
	imgname := "spawnpoint_" + cfg.ServiceName
	if rebuildImg {
		err = rebuildImage(cfg, imgname)
		if err != nil {
			fmt.Printf("Failed to build image for svc %s: %v\n", cfg.ServiceName, err)
			return nil, err
		}
	}

	mounts, err := createMounts(cfg.ServiceName, cfg.Volumes)
	if err != nil {
		fmt.Printf("Failed to set up container volumes: %v\n", err)
	}

	cnt, err := dkr.CreateContainer(docker.CreateContainerOptions{
		Name: "spawnpoint_" + cfg.ServiceName,
		Config: &docker.Config{
			WorkingDir:   "/srv/spawnpoint/",
			OnBuild:      cfg.Build,
			Cmd:          cfg.Run,
			Image:        imgname,
			Env:          []string{"BW2_DEFAULT_ENTITY=/srv/spawnpoint/entity.key"},
			Mounts:       mounts,
			AttachStdout: true,
			AttachStderr: true,
			AttachStdin:  true,
		},
	})
	if err != nil {
		fmt.Println("Error building new container for svc", cfg.ServiceName)
		fmt.Println(err)
		return nil, err
	}

	portBindings := make(map[docker.Port][]docker.PortBinding)
	if strings.HasPrefix(bwRouter, "localhost:") || strings.HasPrefix(bwRouter, "127.0.0.1:") {
		bwPortNum := bwRouter[strings.Index(bwRouter, ":")+1:]
		containerPort := docker.Port(bwPortNum + "/tcp")
		hostPort := docker.PortBinding{HostPort: bwPortNum}
		portBindings[containerPort] = []docker.PortBinding{hostPort}
	}

	err = dkr.StartContainer(cnt.ID, &docker.HostConfig{
		NetworkMode:  "host",
		Memory:       int64(cfg.MemAlloc) * 1024 * 1024,
		CPUShares:    int64(cfg.CPUShares),
		PortBindings: portBindings,
	})
	if err != nil {
		fmt.Println("Failed to start container for svc", cfg.ServiceName)
		return nil, err
	}

	// Attach
	go dkr.AttachToContainer(docker.AttachToContainerOptions{
		Container:    cnt.ID,
		OutputStream: *cfg.logger,
		ErrorStream:  *cfg.logger,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})

	// Collect statistics
	statChan := make(chan *docker.Stats)
	go dkr.Stats(docker.StatsOptions{
		ID:     cnt.ID,
		Stats:  statChan,
		Stream: true,
	})

	return &SpawnPointContainer{cnt, cfg.ServiceName, &statChan}, nil
}

func rebuildImage(cfg *Manifest, imgname string) error {
	// First remove the old image
	err := dkr.RemoveImage(imgname)
	if err != nil && err != docker.ErrNoSuchImage {
		return err
	}

	dfile := []string{"FROM immesys/spawnpoint:amd64"}
	dfile = append(dfile, cfg.Build...)

	tdir, err := ioutil.TempDir("", "spawnpoint")
	tname := tdir + "/dockerfile"
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(tname, []byte(strings.Join(dfile, "\n")), 0666)
	if err != nil {
		return err
	}

	err = dkr.BuildImage(docker.BuildImageOptions{
		Dockerfile:   "dockerfile",
		OutputStream: os.Stdout,
		ContextDir:   tdir,
		Name:         imgname,
		NoCache:      true,
		Pull:         true,
	})
	if err != nil {
		return err
	}
	return nil
}

func createMounts(svcName string, volumeNames []string) ([]docker.Mount, error) {
	volumeList, err := dkr.ListVolumes(docker.ListVolumesOptions{})
	if err != nil {
		return nil, err
	}
	existingVolumes := make(map[string]bool)
	for _, volume := range volumeList {
		existingVolumes[volume.Name] = true
	}

	mounts := make([]docker.Mount, len(volumeNames))
	for i, volumeName := range volumeNames {
		fullVolumeName := svcName + "." + volumeName
		_, ok := existingVolumes[fullVolumeName]
		if !ok {
			_, err = dkr.CreateVolume(docker.CreateVolumeOptions{Name: fullVolumeName})
			if err != nil {
				return nil, err
			}
		}

		mounts[i] = docker.Mount{
			Name:        fullVolumeName,
			Destination: "/srv/" + fullVolumeName,
		}
	}

	return mounts, nil
}
