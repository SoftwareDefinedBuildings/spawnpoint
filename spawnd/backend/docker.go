package backend

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/SoftwareDefinedBuildings/spawnpoint/service"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	docker "github.com/docker/docker/client"
	logging "github.com/op/go-logging"
	"github.com/pkg/errors"
)

const defaultSpawnpointImage = "jhkolb/spawnpoint:amd64"

type Docker struct {
	Alias     string
	bw2Router string
	client    *docker.Client
	logger    *logging.Logger
}

func NewDocker(alias, bw2Router string, logger *logging.Logger) (*Docker, error) {
	client, err := docker.NewEnvClient()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to initialize docker client")
	}

	return &Docker{
		Alias:     alias,
		bw2Router: bw2Router,
		client:    client,
		logger:    logger,
	}, nil
}

func (dkr *Docker) StartService(ctx context.Context, svcConfig *service.Configuration) (chan string, error) {
	baseImage := svcConfig.BaseImage
	if len(baseImage) == 0 {
		svcConfig.BaseImage = defaultSpawnpointImage
	}
	dkr.logger.Debugf("(%s) Building Docker container from image %s", svcConfig.Name, svcConfig.BaseImage)
	imageName, err := dkr.buildImage(ctx, svcConfig)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to build service Docker container")
	}

	dkr.logger.Debugf("(%s) Setting BW2_AGENT=%s", svcConfig.Name, dkr.bw2Router)
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

	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode("bridge"),
	}
	if svcConfig.UseHostNet {
		dkr.logger.Debugf("(%s) Using bridge networking mode", svcConfig.Name)
		hostConfig.NetworkMode = container.NetworkMode("host")
	} else {
		dkr.logger.Debugf("(%s) Using host networking mode", svcConfig.Name)
	}

	containerName := fmt.Sprintf("%s_%s", dkr.Alias, svcConfig.Name)
	dkr.logger.Debugf("(%s) Creating container %s", svcConfig.Name, containerName)
	createdResult, err := dkr.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, containerName)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to create Docker container")
	}
	if err = dkr.client.ContainerStart(ctx, createdResult.ID, types.ContainerStartOptions{}); err != nil {
		return nil, errors.Wrap(err, "Failed to start Docker container")
	}

	dkr.logger.Debugf("(%s) Attaching to container output streams", svcConfig.Name)
	hijackResp, err := dkr.client.ContainerAttach(ctx, containerName, types.ContainerAttachOptions{
		Logs:   true,
		Stream: true,
		Stderr: true,
		Stdout: true,
	})
	if err != nil {
		return nil, errors.Wrap(err, "Failed to attach to Docker container")
	}
	msgChan := make(chan string, 20)
	go func() {
		defer hijackResp.Close()
		for {
			msg, err := hijackResp.Reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					dkr.logger.Errorf("(%s) Failed to read container output: %s", svcConfig.Name, err)
				}
				return
			}
			dkr.logger.Debugf("(%s) Read container output entry, sending to log", svcConfig.Name)
			msgChan <- msg
		}
	}()
	return msgChan, nil
}

func (dkr *Docker) RestartService(ctx context.Context, id string) error {
	return nil
}

func (dkr *Docker) StopService(ctx context.Context, id string) error {
	return nil
}

func (dkr *Docker) buildImage(ctx context.Context, svcConfig *service.Configuration) (string, error) {
	dkr.logger.Debugf("(%s) Generating container build context", svcConfig.Name)
	buildCtxt, err := generateBuildContext(svcConfig)
	if err != nil {
		return "", errors.Wrap(err, "Failed to generate Docker build context")
	}

	imgName := "spawnpoint_" + svcConfig.Name
	dkr.logger.Debugf("(%s) Image Name: %s", svcConfig.Name, imgName)
	_, err = dkr.client.ImageBuild(ctx, buildCtxt, types.ImageBuildOptions{
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
