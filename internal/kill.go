package internal

import (
	"context"
	"os"
)

type KillArgs struct {
	ProjectName    string   `validate:"required,varname"`
	Hosts          []string `validate:"required,min=1"`
	ExperimentName string   `validate:"varname"`
	ContainerName  *string
}

func nameFromKillArgs(args KillArgs) string {
  if args.ContainerName != nil && *args.ContainerName != "" {
		return *args.ContainerName
	}

	return DefaultProjExpContainerName(args.ProjectName, args.ExperimentName)
}

func Kill(args KillArgs) {
	if err := Validator().Struct(args); err != nil {
		panic(err)
	}

	masterAndRankElseExit(args.Hosts)

	// get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}

	cachePath := home + "/.cache/" + args.ProjectName + "/" + "experiments/"

	// get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	dr := NewDockerRun(context.Background(), args.ProjectName, cwd, cachePath)

	if err := dr.Kill(nameFromKillArgs(args)); err != nil {
		panic(err)
	}
}
