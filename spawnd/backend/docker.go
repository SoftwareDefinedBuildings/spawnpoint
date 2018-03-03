package backend

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	docker "github.com/docker/docker/client"
	"github.com/pkg/errors"
)

const defaultSpawnpointImage = "jhkolb/spawnable:amd64"
const logMaxSize = "50m"

type Docker struct {
	Alias     string
	bw2Router string
	client    *docker.Client
}

func NewDocker(alias, bw2Router string) (*Docker, error) {
	client, err := docker.NewEnvClient()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to initialize docker client")
	}

	return &Docker{
		Alias:     alias,
		bw2Router: bw2Router,
		client:    client,
	}, nil
}

func (dkr *Docker) StartService(ctx context.Context, svcConfig *service.Configuration) (string, error) {
	baseImage := svcConfig.BaseImage
	if len(baseImage) == 0 {
		svcConfig.BaseImage = defaultSpawnpointImage
	}
	imageName, err := dkr.buildImage(ctx, svcConfig)
	if err != nil {
		return "", errors.Wrap(err, "Failed to build service Docker container")
	}

	envVars := []string{
		"BW2_DEFAULT_ENTITY=/srv/spawnpoint/entity.key",
		"BW2_AGENT=" + dkr.bw2Router,
	}
	containerConfig := &container.Config{
		Image:        imageName,
		Cmd:          svcConfig.Run,
		WorkingDir:   "/srv/spawnpoint",
		Env:          envVars,
		AttachStderr: true,
		AttachStdout: true,
	}

	mounts, err := dkr.createMounts(ctx, svcConfig.Volumes)
	if err != nil {
		return "", errors.Wrap(err, "Failed to create container volumes")
	}
	devices := make([]container.DeviceMapping, len(svcConfig.Devices))
	for i, devicePath := range svcConfig.Devices {
		devices[i] = container.DeviceMapping{
			PathOnHost:        devicePath,
			PathInContainer:   devicePath,
			CgroupPermissions: "rwm",
		}
	}
	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode("bridge"),
		LogConfig:   container.LogConfig{Config: map[string]string{"max-size": "50m"}},
		Mounts:      mounts,
		Resources: container.Resources{
			CPUShares: int64(svcConfig.CPUShares),
			Memory:    int64(svcConfig.Memory * 1024 * 1024),
			Devices:   devices,
		},
	}
	if svcConfig.UseHostNet {
		hostConfig.NetworkMode = container.NetworkMode("host")
	}

	containerName := fmt.Sprintf("%s_%s", dkr.Alias, svcConfig.Name)
	createdResult, err := dkr.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, containerName)
	if err != nil {
		return "", errors.Wrap(err, "Failed to create Docker container")
	}
	if err = dkr.client.ContainerStart(ctx, createdResult.ID, types.ContainerStartOptions{}); err != nil {
		return "", errors.Wrap(err, "Failed to start Docker container")
	}

	return createdResult.ID, nil
}

func (dkr *Docker) RestartService(ctx context.Context, id string) error {
	if err := dkr.client.ContainerRestart(ctx, id, nil); err != nil {
		return errors.Wrap(err, "Could not restart Docker container")
	}
	return nil
}

func (dkr *Docker) StopService(ctx context.Context, id string) error {
	if err := dkr.client.ContainerStop(ctx, id, nil); err != nil {
		return errors.Wrap(err, "Could not stop Docker container")
	}
	return nil
}

func (dkr *Docker) RemoveService(ctx context.Context, id string) error {
	if err := dkr.client.ContainerRemove(ctx, id, types.ContainerRemoveOptions{}); err != nil {
		return errors.Wrap(err, "Could not remove Docker container")
	}
	return nil
}

func (dkr *Docker) TailService(ctx context.Context, id string, log bool) (<-chan string, <-chan error) {
	msgChan := make(chan string, 20)
	errChan := make(chan error, 1)

	hijackResp, err := dkr.client.ContainerAttach(ctx, id, types.ContainerAttachOptions{
		Logs:   log,
		Stream: true,
		Stderr: true,
		Stdout: true,
	})
	if err != nil {
		close(msgChan)
		errChan <- errors.Wrap(err, "Failed to attach to Docker container")
		return msgChan, errChan
	}

	go func() {
		defer hijackResp.Close()
		for {
			msg, err := hijackResp.Reader.ReadString('\n')
			if err != nil {
				close(msgChan)
				if err != io.EOF {
					errChan <- errors.Wrap(err, "Failed to read container log message")
				}
				return
			}
			msgChan <- msg
		}
	}()

	return msgChan, errChan
}

func (dkr *Docker) MonitorService(ctx context.Context, id string) (<-chan Event, <-chan error) {
	transformedEvChan := make(chan Event, 20)
	transformedErrChan := make(chan error, 1)

	evChan, errChan := dkr.client.Events(ctx, types.EventsOptions{
		Filters: filters.NewArgs(filters.Arg("container", id)),
	})
	// Check for an error right away
	select {
	case err := <-errChan:
		close(transformedEvChan)
		transformedErrChan <- errors.Wrap(err,
			fmt.Sprintf("Failed to initialize event monitor for container %s", id))
	default:
	}

	go func() {
		for {
			select {
			case event := <-evChan:
				switch event.Action {
				case "die":
					transformedEvChan <- Die
				default:
				}
			case err := <-errChan:
				close(transformedEvChan)
				transformedErrChan <- errors.Wrap(err,
					fmt.Sprintf("Failed to retrieve log entry for container %s", id))
			}
		}
	}()

	return transformedEvChan, transformedErrChan
}

func (dkr *Docker) ListServices(ctx context.Context) ([]string, error) {
	containers, err := dkr.client.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "Failed to list running containers")
	}

	IDs := make([]string, len(containers))
	for i, container := range containers {
		IDs[i] = container.ID
	}
	return IDs, nil
}

func (dkr *Docker) buildImage(ctx context.Context, svcConfig *service.Configuration) (string, error) {
	buildCtxt, err := generateBuildContext(svcConfig)
	if err != nil {
		return "", errors.Wrap(err, "Failed to generate Docker build context")
	}

	imgName := "spawnpoint_" + svcConfig.Name
	resp, err := dkr.client.ImageBuild(ctx, buildCtxt, types.ImageBuildOptions{
		Tags:        []string{imgName},
		NoCache:     true,
		Context:     buildCtxt,
		Dockerfile:  "dockerfile",
		Remove:      true,
		ForceRemove: true,
	})
	if err != nil {
		return "", errors.Wrap(err, "Daemon failed to build image")
	}
	defer resp.Body.Close()
	// We have to do this for an image build to actually occur
	ioutil.ReadAll(resp.Body)

	return imgName, nil
}

func generateDockerFile(config *service.Configuration) (*[]byte, error) {
	var dkrFileBuf bytes.Buffer
	dkrFileBuf.WriteString(fmt.Sprintf("FROM %s\n", config.BaseImage))
	if len(config.Source) > 0 {
		sourceparts := strings.SplitN(config.Source, "+", 2)
		switch sourceparts[0] {
		case "git":
			dkrFileBuf.WriteString(fmt.Sprintf("RUN git clone %s /srv/spawnpoint\n", sourceparts[1]))
		default:
			return nil, fmt.Errorf("Unkonwn source type: %s", config.Source)
		}
	}
	dkrFileBuf.WriteString("WORKDIR /srv/spawnpoint\n")
	dkrFileBuf.WriteString("COPY entity.key entity.key\n")
	for _, includedFile := range config.IncludedFiles[:len(config.IncludedFiles)-1] {
		dkrFileBuf.WriteString(fmt.Sprintf("COPY %s %s\n", includedFile, includedFile))
	}
	for _, includedDir := range config.IncludedDirectories {
		baseName := filepath.Base(includedDir)
		dkrFileBuf.WriteString(fmt.Sprintf("COPY %s %s\n", baseName, baseName))
	}

	for _, buildCmd := range config.Build {
		dkrFileBuf.WriteString(fmt.Sprintf("RUN %s\n", buildCmd))
	}

	contents := dkrFileBuf.Bytes()
	return &contents, nil
}

func generateBuildContext(config *service.Configuration) (io.Reader, error) {
	var buildCtxtBuffer bytes.Buffer
	tarWriter := tar.NewWriter(&buildCtxtBuffer)

	// Add Bosswave entity to the build context
	entity, err := base64.StdEncoding.DecodeString(config.BW2Entity)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to decode BW2 entity")
	}
	if err = tarWriter.WriteHeader(&tar.Header{
		Name: "entity.key",
		Size: int64(len(entity)),
	}); err != nil {
		return nil, errors.Wrap(err, "Failed to write entity file tar header")
	}
	if _, err = tarWriter.Write(entity); err != nil {
		return nil, errors.Wrap(err, "Failed to write entity file to tar")
	}

	// Add synthetic dockerfile to the build context
	dkrFileContents, err := generateDockerFile(config)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to generate dockerfile contents")
	}
	if err = tarWriter.WriteHeader(&tar.Header{
		Name: "dockerfile",
		Size: int64(len(*dkrFileContents)),
	}); err != nil {
		return nil, errors.Wrap(err, "Failed to write Dockerfile tar header")
	}
	if _, err = tarWriter.Write(*dkrFileContents); err != nil {
		return nil, errors.Wrap(err, "Failed to write Dockerfile to tar")
	}

	// Add any included files or directories to build context
	if len(config.IncludedFiles) > 0 {
		encoding := config.IncludedFiles[len(config.IncludedFiles)-1]
		decodedFiles, err := base64.StdEncoding.DecodeString(encoding)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to decode included files")
		}
		decodedFilesBuf := bytes.NewBuffer(decodedFiles)
		tarReader := tar.NewReader(decodedFilesBuf)
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				return nil, errors.Wrap(err, "Failed to read included files archive")
			}
			tarWriter.WriteHeader(header)
			io.Copy(tarWriter, tarReader)
		}
	}

	return &buildCtxtBuffer, nil
}

func (dkr *Docker) createMounts(ctx context.Context, volumeNames []string) ([]mount.Mount, error) {
	existingVolumesResp, err := dkr.client.VolumeList(ctx, filters.Args{})
	if err != nil {
		return nil, errors.Wrap(err, "Failed to list docker volumes")
	}
	existingVolumes := createVolumeNameSet(existingVolumesResp.Volumes)

	mounts := make([]mount.Mount, len(volumeNames))
	for i, volumeName := range volumeNames {
		if _, ok := existingVolumes[volumeName]; !ok {
			if _, err := dkr.client.VolumeCreate(ctx, volume.VolumesCreateBody{Name: volumeName}); err != nil {
				return nil, errors.Wrap(err, "Failed to create Docker volume")
			}
		}
		mounts[i] = mount.Mount{
			Type:   mount.TypeVolume,
			Source: volumeName,
			Target: "/srv/" + volumeName,
		}
	}
	return mounts, nil
}

func createVolumeNameSet(volumes []*types.Volume) map[string]struct{} {
	retVal := make(map[string]struct{})
	for _, volume := range volumes {
		retVal[volume.Name] = struct{}{}
	}
	return retVal
}
