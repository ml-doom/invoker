package internal

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	envparse "github.com/hashicorp/go-envparse"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"slices"

	"path/filepath"
)

const url = "https://api.ipify.org"

func PtrTo[T any](e T) *T {
	return &e
}

func loadEnvFile(path string) ([]string, error) {
	// check if the file exists
	// if it does not exist, return an empty slice
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []string{}, nil
	}

	// open file
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to open env file")
	}

	defer file.Close()

	envs, err := envparse.Parse(file)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to parse env file")
	}

	var lines []string
	for key, value := range envs {
		lines = append(lines, key+"="+value)
	}

	return lines, nil
}

func myPublicIP() (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", errors.WithMessage(err, "failed to get public IP")
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", errors.WithMessage(err, "failed to read response body")
	}

	return string(body), nil
}

func localIPs() ([]string, error) {
	var ips []string
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addresses {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
	}
	return ips, nil
}

var ErrOmitHost = errors.New("ip not found in hosts list")

func masterAndRank(hosts []string) (string, int, error) {
	ip, err := myPublicIP()
	if err != nil {
		return "", 0, errors.WithMessage(err, "failed to get public IP")
	}
	ips := []string{ip}

	localIPs, err := localIPs()
	if err != nil {
		return "", 0, errors.WithMessage(err, "failed to get local IPs")
	}

	ips = append(ips, localIPs...)

	master, rank := hosts[0], -1
	for i, host := range hosts {
		if slices.Contains(ips, host) {
			rank = i
			break
		}
	}

	if len(hosts) == 1 && master == "localhost" {
		return master, 1, nil
	}

	if rank == -1 {
		return "", 0, ErrOmitHost
	}

	return master, rank, nil
}

func masterAndRankElseExit(hosts []string) (string, int) {
	ip, rank, err := masterAndRank(hosts)
	if err != nil {
		if errors.Is(err, ErrOmitHost) {
			fmt.Printf("%s not found in hosts list, omitting\n", ip)
			os.Exit(0)
		}

		fmt.Println(err)
		os.Exit(1)
	}

	return ip, rank
}

func portIsAvailable(port int) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Printf("port %d is already in use\n", port)
		fmt.Println(err)
		os.Exit(1)
	}

	defer listener.Close()
}

type Path struct {
	path string
}

func (p *Path) mkdirIfNotExists() error {
	// Check if the directory already exists
	_, err := os.Stat(p.path)
	if os.IsNotExist(err) {
		// Directory doesn't exist, so create it
		err := os.MkdirAll(p.path, os.ModePerm)
		if err != nil {
			return errors.WithMessagef(err, "failed to create directory %s", p.path)
		}
	} else if err != nil {
		return errors.WithMessagef(err, "failed to stat directory %s", p.path)
	}

	return nil
}

func (p *Path) Join(subpath string) *Path {
	return &Path{path: filepath.Join(p.path, subpath)}
}

func makeDefaultDirectories(projectName, experimentName, runName string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", errors.WithMessage(err, "failed to get user home directory")
	}

	cacheDir := Path{path: filepath.Join(home, ".cache")}
	if err = cacheDir.mkdirIfNotExists(); err != nil {
		return "", "", errors.WithMessage(err, "failed to create cache directory")
	}

	checkpointDir := cacheDir.Join("higgsfield").Join(projectName).Join("experiments").Join(experimentName).Join(runName)
	if err = checkpointDir.mkdirIfNotExists(); err != nil {
		return "", "", errors.WithMessagef(err, "failed to create checkpoint directory for experiment %s and run name %s", experimentName, runName)
	}

	return cacheDir.path, checkpointDir.path, nil
}

func exitIfError(flag string, err error) {
	if err != nil {
		fmt.Printf("cannot parse %s: %v\n", flag, err)
		os.Exit(1)
	}
}

func nothingIfError(flag string, err error) {}

func ParseOrNil[T ~string | ~int | ~[]string](cmd *cobra.Command, flag string) *T {
	// TODO: buddy, need to fix this
	got, ok := parseOrExitInternal[T](cmd, flag, false)
	if !ok {
		return nil
	}
	return PtrTo(got.(T))
}

func ParseOrExit[T ~string | ~int | ~[]string](cmd *cobra.Command, flag string) T {
	got, _ := parseOrExitInternal[T](cmd, flag, true)
	return got.(T)
}

func parseOrExitInternal[T ~string | ~int | ~[]string](cmd *cobra.Command, flag string, exit bool) (interface{}, bool) {
	errFunc := nothingIfError

	if exit {
		errFunc = exitIfError
	}

	var value T
	switch v := any(value).(type) {
	case string:
		v, err := cmd.Flags().GetString(flag)
		errFunc(flag, err)
		return v, err == nil
	case int:
		v, err := cmd.Flags().GetInt(flag)
		errFunc(flag, err)
		return v, err == nil
	case []string:
		v, err := cmd.Flags().GetStringSlice(flag)
		errFunc(flag, err)
		return v, err == nil
	default:
		fmt.Printf("cannot parse %s: unknown type %T\n", flag, v)
		os.Exit(1)
	}

	return nil, false
}

const trainInfoFormat = `
╔══════════════════════════════════════════════════════════════════════════════════════════════════════
║  
║  > Training info:
║  > 🛠🛠🛠
║    
║  > EXPERIMENT NAME  = %s 
║  > RUN NAME         = %s
║  > CONTAINER NAME   = %s
║  > MODEL CHKPT PATH = %s
║
╚══════════════════════════════════════════════════════════════════════════════════════════════════════
`

func trimPathForLength(path string, length int) string {
	// check if path is less than length
	if len(path) < length {
		return path
	}

	// get rid of home directory and replace is with ~
	// e.g. /home/user/... -> ~/...
	if path[0] == '/' {
		path = path[1:]
	}

	branches := strings.Split(path, "/")
	slashes := len(branches) - 1
	if slashes == 0 {
		return path[:length]
	}

	if branches[0] == "home" {
		path = "~/" + strings.Join(branches[2:], "/")
	}

	if len(path) < length {
		return path
	}

	return path[:length] + "..."
}

func MapJoin[T comparable, V any](maps ...map[T]V) map[T]V {
	newMap := make(map[T]V)
	for _, m := range maps {
		for k, v := range m {
			newMap[k] = v
		}
	}

	return newMap
}

func ValueOf[T any](ptr *T) T {
	return *ptr
}

func FirstOfMap[T comparable, V any](m map[T]V) (T, V, bool) {
	for k, v := range m {
		return k, v, true
	}
	return ValueOf(new(T)), ValueOf(new(V)), false
}

func FirstKey[T comparable, V any](m map[T]V) (T, bool) {
	for k := range m {
		return k, true
	}
	return ValueOf(new(T)), false
}

func FirstValue[T comparable, V any](m map[T]V) (V, bool) {
	for _, v := range m {
		return v, true
	}
	return ValueOf(new(V)), false
}

func Keys[T comparable, V any](m map[T]V) []T {
	keys := make([]T, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
