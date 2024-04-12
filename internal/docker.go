package internal

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	units "github.com/docker/go-units"
	"github.com/pkg/errors"
	"path/filepath"
)

type DockerRun struct {
	client                *client.Client
	ctx                   context.Context
	projectName           string
	guestRootPath         string
	guestCachePath        string
	guestProjectCachePath string
	imageTag              string
	hostRootPath          string
	hostCachePath         string
	hostGID               int
	hostUID               int
}

func NewDockerRun(
	ctx context.Context,
	projectName,
	hostRootPath,
	hostCachePath string,
) *DockerRun {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	hostGID := os.Getgid()
	hostUID := os.Getuid()

	return &DockerRun{
		client:                cli,
		ctx:                   ctx,
		projectName:           projectName,
		guestRootPath:         guestRootPath,
		guestCachePath:        guestCachePath,
		guestProjectCachePath: guestCachePath + projectName,
		imageTag:              imageTag,
		hostRootPath:          hostRootPath,
		hostCachePath:         hostCachePath,
		hostGID:               hostGID,
		hostUID:               hostUID,
	}
}

func (d *DockerRun) Kill(containerName string) error {
	options := types.ContainerListOptions{All: true, Filters: filters.NewArgs(filters.Arg("name", containerName))}

	containers, err := d.client.ContainerList(d.ctx, options)
	if err != nil {
		return errors.WithMessagef(err, "failed to list containers with name %s", containerName)
	}

	fmt.Printf("found %d containers with name %s\n", len(containers), containerName)

	for _, c := range containers {
		if c.Status == "running" {
			fmt.Printf("stopping container %s\n", c.ID)
			if err := d.client.ContainerStop(d.ctx, c.ID, container.StopOptions{Timeout: PtrTo(0)}); err != nil {
				fmt.Printf("failed to stop container %s, reason: %v", c.ID, err)
			}
		}

		fmt.Printf("removing container %s\n", c.ID)
		if err := d.client.ContainerRemove(d.ctx, c.ID, types.ContainerRemoveOptions{Force: true}); err != nil {
			return errors.WithMessagef(err, "failed to remove container %s", c.ID)
		}
	}

	return nil
}

func (d *DockerRun) Run(
	containerName string,
	runCommand string,
	runCommandArgs []string,
	exposePort int,
) error {
	fmt.Printf("killing container %s\n", containerName)
	if err := d.Kill(containerName); err != nil {
		return errors.WithMessagef(err, "failed to kill container %s", containerName)
	}

	if err := d.build(); err != nil {
		return errors.WithMessagef(err, "failed to build image %s", d.imageTag)
	}

	dm, dr := d.deviceMapsAndRequests()
	envVars, err := loadEnvFile(filepath.Join(d.hostRootPath, "nccl_config_env"))
	if err != nil {
		return errors.WithMessagef(err, "failed to load nccl_config_env file")
	}

	fmt.Printf("creating container %s\n", containerName)
	createOptions := types.ContainerCreateConfig{
		Name: containerName,
		Config: &container.Config{
			Image:      d.imageTag,
			Entrypoint: append([]string{runCommand}, runCommandArgs...),
			Env:        envVars,
		},
		HostConfig: &container.HostConfig{
			Binds:       d.volbinds(),
			IpcMode:     container.IPCModeHost,
			PidMode:     container.PidMode("host"),
			NetworkMode: container.NetworkMode("host"),
			CapAdd:      capAdd(),
			Resources: container.Resources{
				DeviceRequests: dr,
				Ulimits: []*units.Ulimit{
					{
						Name: "memlock",
						Soft: -1,
						Hard: -1,
					},
					{
						Name: "stack",
						Soft: 67108864,
						Hard: 67108864,
					},
				},
				Devices: dm,
			},
			Privileged: true,
		},
	}

	resp, err := d.client.ContainerCreate(d.ctx, createOptions.Config, createOptions.HostConfig, nil, nil, containerName)
	if err != nil {
		return errors.WithMessagef(err, "failed to create container %s", containerName)
	}

	fmt.Printf("starting container %s\n", containerName)
	if err := d.client.ContainerStart(d.ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return errors.WithMessagef(err, "failed to start container %s", containerName)
	}

	fmt.Printf("started container %s\n", containerName)

	return nil
}
