package internal

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"os"
	"strings"
)

type RunArgs struct {
	ProjectName    string   `validate:"required,varname"`
	Hosts          []string `validate:"required"`
	NProcPerNode   int      `validate:"required,min=1"`
	ExperimentName string   `validate:"required,varname"`
	Port           int      `validate:"required,min=1"`
	RunName        string   `validate:"required,varname"`
	MaxRepeats     int      `validate:"required,min=-1"`
	Rest           []string
	ContainerName  *string
	MasterHost     *string `validate:"omitempty,ip"`
	NoPython       *string
}

func (r *RunArgs) Restartable() State {
	// the Rest might have "hf_action_restartable" in it
	// if it does, then parse the value and cast it to State
	// otherwise return Stoppable, which must be a default value anyway

	if len(r.Rest) == 0 {
		return Stoppable
	}

	for _, arg := range r.Rest {
		// hf_action_restartable="running"
		if strings.HasPrefix(arg, "hf_action_restartable") {
			// split the value by "=" and get the second part
			// which is the state
			state := strings.Split(arg, "=")[1]
			// if the state is "running" then return Running
			// otherwise return Stoppable
			if state == "running" {
				return Running
			}
		}
	}

	return Stoppable
}

const runScript = `#!/usr/bin/env python
from higgsfield.internal.main import cli;
cli()
`

func nameFromRunArgs(args RunArgs) string {
	if args.ContainerName != nil && *args.ContainerName != "" {
		return *args.ContainerName
	}

	return DefaultProjExpContainerName(args.ProjectName, args.ExperimentName)
}

func masterHostElseFirstHost(args RunArgs) string {
	// If MasterHost is provided, return it
	if args.MasterHost != nil && *args.MasterHost != "" {
		return *args.MasterHost
	}

	return args.Hosts[0]
}

func noPythonOpt(args RunArgs) []string {
	if args.NoPython != nil && *args.NoPython != "" {
		return []string{"--no-python", *args.NoPython, "python"}
	}

	return []string{}
}

func Run(args RunArgs) {
	if err := Validator().Struct(args); err != nil {
		panic(err)
	}

	master := masterHostElseFirstHost(args)
	rank := 0

	if len(args.Hosts) > 1 {
		_, rank = masterAndRankElseExit(args.Hosts)
	} else {
		master = "localhost"
	}

	nodeNum := len(args.Hosts)

	// we need to check port only on the master host
	if rank == 0 {
		portIsAvailable(args.Port)
	}

	hostCachePath, checkpointDir, err := makeDefaultDirectories(
		args.ProjectName, args.ExperimentName, args.RunName)
	if err != nil {
		fmt.Printf("failed to create directories: %v\n", err)
		os.Exit(1)
	}

	containerName := nameFromRunArgs(args)

	fmt.Printf(
		trainInfoFormat,
		args.ExperimentName,
		args.RunName,
		containerName,
		trimPathForLength(checkpointDir, 70))

	cmd, cmdArgs := buildArgs(
		nodeNum,
		rank,
		master,
		args.Port,
		noPythonOpt(args),
		[]string{"hf.py", "run"},
		args.NProcPerNode,
		args.ExperimentName,
		args.RunName,
		args.MaxRepeats,
		args.Rest,
	)

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("failed to get current working directory: %v\n", err)
		os.Exit(1)
	}

	// create a "higgsfield" file in cwd
	f, err := os.Create("hf.py")
	if err != nil {
		fmt.Printf("failed to create a file: %v\n", err)
	}
	defer f.Close()

	f.Write([]byte(runScript))

	dr := NewDockerRun(context.Background(), args.ProjectName, cwd, hostCachePath)
	if err := dr.Run(containerName, cmd, cmdArgs, args.Port); err != nil {
		fmt.Printf("error occured while running experiment: %+v\n", err)
		os.Exit(1)
	}
}

func buildArgs(
	nodeNum int,
	rank int,
	master string,
	masterPort int,
	nopt []string,
	experimentExecutable []string,
	nProcPerNode int,
	experimentName string,
	runName string,
	maxRepeats int,
	rest []string,
) (string, []string) {
	args := []string{
		"--nnodes",
		fmt.Sprint(nodeNum),
		"--node_rank",
		fmt.Sprint(rank),
		"--nproc_per_node",
		fmt.Sprint(nProcPerNode),
	}

	if master != "localhost" {
		args = append(args,
			"--master_addr",
			master,
			"--master_port",
			fmt.Sprint(masterPort),
		)
	}

	if len(nopt) > 0 {
		args = append(args, nopt...)
	}

	args = append(args, experimentExecutable...)
	args = append(args,
		"--experiment_name",
		experimentName,
		"--run_name",
		runName,
		"--max_repeats",
		fmt.Sprint(maxRepeats))
	args = append(args, rest...)

	return "torchrun", args
}

func (r RunArgs) Equal(a RunArgs) bool {
	// serializes as gob, then compares the bytes
	// if they are equal, then the structs are equal

	var buf1, buf2 bytes.Buffer
	if err := gob.NewEncoder(&buf1).Encode(r); err != nil {
		fmt.Printf("error encoding run args left: %v\n", err)
		// should I return false?
	}
	if err := gob.NewEncoder(&buf2).Encode(a); err != nil {
		fmt.Printf("error encoding run args right: %v\n", err)
	}

	return bytes.Equal(buf1.Bytes(), buf2.Bytes())
}
