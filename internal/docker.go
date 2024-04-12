package internal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
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

const (
	imageTag           = "hf-torch:latest"
	guestRootPath      = "/srv/"
	guestCachePath     = "/home/nonroot/.cache/"
	guestRootCachePath = "/root/.cache/"
)

func isCos() (bool, error) {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return false, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ID=") {
			id := strings.TrimPrefix(line, "ID=")
			return id == "cos", nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("failed to scan file: %w", err)
	}

	return false, nil
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

func DefaultProjExpContainerName(projectName, experimentName string) string {
	return fmt.Sprintf("%s-%s", projectName, experimentName)
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

var otherNvidiaDevices = []string{
	"/dev/nvidia-uvm",
	"/dev/nvidiactl",

	// not really sure if we need these
	"/dev/nvidia-modeset",
	"/dev/nvidia-uvm-tools",
}

func listOtherNvidiaDevices() []string {
	devices := make([]string, 0, len(otherNvidiaDevices))
	for _, path := range otherNvidiaDevices {
		if _, err := os.Stat(path); err == nil {
			devices = append(devices, path)
		}
	}

	return devices
}

func listNvidiaGPUs() []string {
	gpus := make([]string, 0, 32)
	// we just need to check whether /dev/nvidia%d exists
	for i := 0; i < 32; i++ {
		path := fmt.Sprintf("/dev/nvidia%d", i)
		if _, err := os.Stat(path); err == nil {
			gpus = append(gpus, path)
		}
	}

	return gpus
}

func createDeviceMapping(devices []string) []container.DeviceMapping {
	mappings := make([]container.DeviceMapping, 0, len(devices))
	for _, path := range devices {
		mappings = append(mappings, container.DeviceMapping{
			PathOnHost:        path,
			PathInContainer:   path,
			CgroupPermissions: "rwm",
		})
	}
	return mappings
}

var ldMap = map[string]string{
	"/var/lib/nvidia/lib64": "/usr/local/nvidia/lib64",
	"/var/lib/tcpx":         "/usr/local/tcpx",
	"/run/tcpx":             "/run/tcpx",
}

func ldBinds() []string {
	binds := make([]string, 0, len(ldMap))
	for host, guest := range ldMap {
		// check if host path exists
		if _, err := os.Stat(host); err != nil {
			continue
		}

		fmt.Printf("adding bind: %s:%s\n", host, guest)

		binds = append(binds, fmt.Sprintf("%s:%s", host, guest))
	}

	return binds
}

func capAdd() []string {
	return []string{
		"NET_ADMIN",
		"SYS_ADMIN",
		"SYS_PTRACE",
		"IPC_LOCK",
	}
}

func (d *DockerRun) volbinds() []string {
	binds := []string{
		fmt.Sprintf("%s:%s", d.hostRootPath, d.guestRootPath),
		fmt.Sprintf("%s:%s", d.hostCachePath, d.guestCachePath),
		fmt.Sprintf("%s:%s", d.hostCachePath, guestRootCachePath),
	}

	binds = append(binds, ldBinds()...)

	return binds
}

func (d *DockerRun) deviceMapsAndRequests() ([]container.DeviceMapping, []container.DeviceRequest) {
	// You can't run invoker on cos that natively, but there's still a workaround :D
	cos, _ := isCos()

	// check if host has gpu
	// if yes, add gpu to device requests
	// else, don't add gpu to device requests
	// this is a hacky way to get around the fact that docker doesn't support
	// gpu passthrough on macos
	dr := make([]container.DeviceRequest, 0, 1)
	dm := make([]container.DeviceMapping, 0, 1)
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		fmt.Printf("host has gpu, adding gpu to device requests\n")
		if !cos {
			dr = append(dr, container.DeviceRequest{
				Count:        -1,
				Capabilities: [][]string{{"gpu"}},
			})
		}
		// usually there's no need to add additional devices on bare-metal
		// but with tcpx setup we need to add other nvidia-ish devices
		dm = append(dm, createDeviceMapping(listNvidiaGPUs())...)
		dm = append(dm, createDeviceMapping(listOtherNvidiaDevices())...)
	} else {
		fmt.Printf("host does not have gpu, not adding gpu to device requests\n")
	}

	return dm, dr
}

func (d *DockerRun) build() error {
	buildCtx, err := archive.TarWithOptions(d.hostRootPath, &archive.TarOptions{})
	if err != nil {
		panic(err)
	}
	defer buildCtx.Close()

	fmt.Printf("rebuilding image %s\n", d.imageTag)
	buildOptions := types.ImageBuildOptions{
		Tags: []string{d.imageTag},
		BuildArgs: map[string]*string{
			"GID": PtrTo(fmt.Sprintf("%d", d.hostGID)),
			"UID": PtrTo(fmt.Sprintf("%d", d.hostUID)),
		},
		Remove:      true, // Remove intermediate containers after the build
		ForceRemove: true, // Force removal of the image if it exists
	}

	buildResponse, err := d.client.ImageBuild(d.ctx, buildCtx, buildOptions)
	if err != nil {
		return errors.WithMessagef(err, "failed to build image %s", d.imageTag)
	}

	defer buildResponse.Body.Close()

	fmt.Printf("building image %s\n", d.imageTag)
	if _, err := io.Copy(os.Stdout, buildResponse.Body); err != nil {
		return errors.WithMessagef(err, "failed to build image %s", d.imageTag)
	}

	return nil
}

func (d *DockerRun) LoadLogs(
	path string,
	rank int,
) (*container.LogConfig, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	cfg, err := ParseLogConfig(path)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to load log config from %s", path)
	}

	if cfg.Type == "awslogs" {
		SetAWSStreamPrefix(cfg, rank)
	}

	return cfg, nil
}

func (d *DockerRun) HostConfigBuilder(
	logCfgPath string, rank int,
) (*HostCfgBuilder, error) {
	dm, dr := d.deviceMapsAndRequests()

	hcfgBuilder := NewHostCfgBuilder().AttachBindsToHostConfig(d.volbinds()).
		AttachPrivilegedToHostConfig(true).
		AttachIPCModeToHostConfig(container.IPCModeHost).
		AttachPidModeToHostConfig(container.PidMode("host")).
		AttachNetworkModeToHostConfig(container.NetworkMode("host")).
		AttachCapAddToHostConfig(capAdd()).
		AttachResourcesToHostConfig(container.Resources{
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
		}).
		AttachPrivilegedToHostConfig(true)

	logCfg, err := d.LoadLogs(filepath.Join(d.hostRootPath, logCfgPath), rank)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to load log config")
	}

	if logCfg != nil {
		hcfgBuilder.AttachLogConfigToHostConfig(*logCfg)
	}

	return hcfgBuilder, nil
}

func (d *DockerRun) Run(
	containerName string,
	runCommand string,
	runCommandArgs []string,
	exposePort int,
	rank int,
) error {
	fmt.Printf("killing container %s\n", containerName)
	if err := d.Kill(containerName); err != nil {
		return errors.WithMessagef(err, "failed to kill container %s", containerName)
	}

	if err := d.build(); err != nil {
		return errors.WithMessagef(err, "failed to build image %s", d.imageTag)
	}

	ncclConfigEnvMap, err := loadEnvMap(filepath.Join(d.hostRootPath, "nccl_config_env"))
	if err != nil {
		return errors.WithMessagef(err, "failed to load nccl_config_env file")
	}

	defEnvMap, _ := loadEnvMap(filepath.Join(d.hostRootPath, "env"))
	envVars := joinEnvMap2Slice(joinMaps(ncclConfigEnvMap, defEnvMap))
  
  hcfgBuilder, err := d.HostConfigBuilder("log_cfg", rank)
  if err != nil {
    return errors.WithMessagef(err, "failed to build host config")
  }

	fmt.Printf("creating container %s\n", containerName)
	createOptions := types.ContainerCreateConfig{
		Name: containerName,
		Config: &container.Config{
			Image:      d.imageTag,
			Entrypoint: append([]string{runCommand}, runCommandArgs...),
			Env:        envVars,
		},
		HostConfig: hcfgBuilder.Build(),
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
