package internal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/pkg/errors"
	"modernc.org/libc/signal"
)

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

func DefaultProjExpContainerName(projectName, experimentName string) string {
	return fmt.Sprintf("%s-%s", projectName, experimentName)
}

var exitCodeRegexp = regexp.MustCompile(`Exited \((\d+)\)`)

func getExitCode(status string) (int, error) {
	match := exitCodeRegexp.FindStringSubmatch(status)
	if len(match) > 0 {
		exitCodeStr := match[1]
		exitCode, err := strconv.Atoi(exitCodeStr)
		if err != nil {
			return 0, errors.WithMessagef(err, "failed to convert exit code %s to int", exitCodeStr)
		}

		return exitCode, nil
	}

	return 0, nil
}

var ErrContainerNotFound = errors.New("container not found")

func containerStateAndExitCode(
	ctx context.Context,
	client *client.Client,
	containerName string,
) (string, int, error) {
	if containerName == "" {
		return "", 0, fmt.Errorf("container name is empty")
	}

	if client == nil {
		return "", 0, fmt.Errorf("client is nil")
	}

	options := types.ContainerListOptions{All: true, Filters: filters.NewArgs(filters.Arg("name", containerName))}
	containers, err := client.ContainerList(ctx, options)
	if err != nil {
		return "", 0, errors.WithMessagef(err, "failed to list containers with name %s", containerName)
	}

	fmt.Printf("found %d containers with name %s\n", len(containers), containerName)

	for _, c := range containers {
		// we expect the exact match
		if c.Names[0] == "/"+containerName {
			exitCode, err := getExitCode(c.Status)
			if err != nil {
				return "", 0, errors.WithMessagef(err, "failed to get exit code for container %s", containerName)
			}

			return c.State, exitCode, nil
		}
	}

  // if container not found, but should've been found, then 1 (aka failed)
	return "", 1, errors.WithMessagef(ErrContainerNotFound, "container %s not found", containerName)
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
