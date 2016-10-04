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

func GetSpawnedContainers() (map[string]*SpawnPointContainer, error) {
	rawrv, err := dkr.ListContainers(docker.ListContainersOptions{
		All: true,
	})
	if err != nil {
		return nil, err
	}

	rv := make(map[string]*SpawnPointContainer)
	for _, containerInfo := range rawrv {
		for _, name := range containerInfo.Names {
			if strings.HasPrefix(name, "/spawnpoint_") {
				container, err := dkr.InspectContainer(containerInfo.ID)
				if err != nil {
					return nil, err
				}

				svcName := name[len("/spawnpoint_"):]
				rv[svcName] = &SpawnPointContainer{
					Raw:         container,
					ServiceName: svcName,
				}
				break
			}
		}
	}

	return rv, nil
}

func StopContainer(serviceName string, removeContainer bool) error {
	curContainers, err := GetSpawnedContainers()
	if err != nil {
		return err
	}

	for name, containerInfo := range curContainers {
		if name == serviceName {
			fmt.Println("Found existing container for service, stopping")
			if containerInfo.Raw.State.Running {
				err := dkr.StopContainer(containerInfo.Raw.ID, timeoutLen)
				if err != nil {
					return err
				}
			}

			if removeContainer {
				fmt.Println("Removing existing container for svc", serviceName)
				err := dkr.RemoveContainer(docker.RemoveContainerOptions{
					ID:    containerInfo.Raw.ID,
					Force: false,
				})
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func RestartContainer(cfg *Manifest, bwRouter string, rebuildImg bool) (*SpawnPointContainer, error) {
	// First remove previous container
	fmt.Println("Restarting container for svc", cfg.ServiceName)
	err := StopContainer(cfg.ServiceName, true)
	if err != nil {
		fmt.Println("Failed to stop container for svc", cfg.ServiceName)
		return nil, err
	}

	// Then rebuild image if necessary
	imgname := "spawnpoint_image_" + cfg.ServiceName
	if rebuildImg {
		err = rebuildImage(cfg, imgname)
		if err != nil {
			return nil, err
		}
	}

	mounts, err := createMounts(cfg.ServiceName, cfg.Volumes)
	if err != nil {
		fmt.Printf("Failed to set up container volumes: %v\n", err)
	}
	envVars := []string{"BW2_DEFAULT_ENTITY=/srv/spawnpoint/entity.key"}
	if bwRouter != "" {
		envVars = append(envVars, "BW2_AGENT="+bwRouter)
	}
	netMode := "bridge"
	if cfg.OverlayNet != "" {
		netMode = cfg.OverlayNet
		if err = createNetIfNecessary(cfg.OverlayNet); err != nil {
			return nil, fmt.Errorf("Failed to create overlay net: %v", err)
		}
	}

	cnt, err := dkr.CreateContainer(docker.CreateContainerOptions{
		Name: "spawnpoint_" + cfg.ServiceName,
		Config: &docker.Config{
			WorkingDir:   "/srv/spawnpoint/",
			OnBuild:      cfg.Build,
			Cmd:          cfg.Run,
			Image:        imgname,
			Env:          envVars,
			Mounts:       mounts,
			AttachStdout: true,
			AttachStderr: true,
			AttachStdin:  true,
		},
		HostConfig: &docker.HostConfig{
			NetworkMode: netMode,
			Memory:      int64(cfg.MemAlloc) * 1024 * 1024,
			CPUShares:   int64(cfg.CPUShares),
		},
	})
	if err != nil {
		fmt.Println("Error building new container for svc", cfg.ServiceName)
		fmt.Println(err)
		return nil, err
	}

	err = dkr.StartContainer(cnt.ID, nil)
	if err != nil {
		fmt.Println("Failed to start container for svc", cfg.ServiceName)
		return nil, err
	}

	attachLogger(cnt, cfg.logger)
	statChan := collectStats(cnt)
	return &SpawnPointContainer{cnt, cfg.ServiceName, statChan}, nil
}

func ReclaimContainer(mfst *Manifest, container *docker.Container) *SpawnPointContainer {
	attachLogger(container, mfst.logger)
	statChan := collectStats(container)
	return &SpawnPointContainer{container, mfst.ServiceName, statChan}
}

func rebuildImage(cfg *Manifest, imgname string) error {
	// First remove the old image
	err := dkr.RemoveImage(imgname)
	if err != nil && err != docker.ErrNoSuchImage {
		return err
	}

	dfile := []string{"FROM " + cfg.Image}
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

func attachLogger(container *docker.Container, logger *BWLogger) {
	go dkr.AttachToContainer(docker.AttachToContainerOptions{
		Container:    container.ID,
		OutputStream: logger,
		ErrorStream:  logger,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})
}

func collectStats(container *docker.Container) *chan (*docker.Stats) {
	statChan := make(chan *docker.Stats)
	go dkr.Stats(docker.StatsOptions{
		ID:     container.ID,
		Stats:  statChan,
		Stream: true,
	})

	return &statChan
}

func createNetIfNecessary(netName string) error {
	networks, err := dkr.ListNetworks()
	if err != nil {
		return err
	}

	for _, network := range networks {
		if network.Name == netName {
			return nil
		}
	}

	_, err = dkr.CreateNetwork(docker.CreateNetworkOptions{
		Name:   netName,
		Driver: "overlay",
	})
	return err
}
