package internal

import (
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"os"
	"strings"

	"path/filepath"
)

type State string

const (
	Running    State = "running"
	Stoppable    State = "stoppable"
)

func isValidState(state string) bool {
	switch state {
	case "running", "stoppable":
		return true
	}

	return false
}

type ProjectExperimentState struct {
	ProjectName    string
	ExperimentName string
	State          State
	RunArgs        RunArgs
}

func (p *ProjectExperimentState) Name() string {
	return p.ProjectName + "-" + p.ExperimentName
}

func (p *ProjectExperimentState) NameAsType() ProjectExperimentStr {
  return ProjectExperimentStr(p.Name())
}

func (p *ProjectExperimentState) Write(restartPath string) error {
	stateFile := filepath.Join(
		restartPath,
		fmt.Sprintf("%s.%s.%s", p.ProjectName, p.ExperimentName, string(p.State)),
	)

	file, err := os.Create(stateFile)
	if err != nil {
		 return errors.WithMessagef(err, "failed to create state file %s", stateFile)
	}
	defer file.Close()

  // dump runArgs as json into the file
  runArgsJson, err := json.Marshal(p.RunArgs)
  if err != nil {
    return errors.WithMessagef(err, "failed to marshal runArgs")
  }

  _, err = file.Write(runArgsJson)
  if err != nil {
    return errors.WithMessagef(err, "failed to write runArgs to file")
  }

  return nil
}

type InnerStateManager struct {
	defaultRestartPath string
	States             map[ProjectExperimentStr]ProjectExperimentState
}

func NewInnerStateManager(restartPath string) (*InnerStateManager, error) {
	// create restartPath if it does not exist
  if err := os.MkdirAll(restartPath, os.ModePerm); err != nil {
    return nil, errors.WithMessagef(err, "failed to create restart directory")
  }

	return &InnerStateManager{
		defaultRestartPath: restartPath,
	}, nil
}

const restartDir = "/tmp/invoker-states"

func NewInnerStateManagerWithDefPath() (*InnerStateManager, error) {
	return NewInnerStateManager(restartDir)
}

func (r *InnerStateManager) readState(stateFile string) (*ProjectExperimentState, error) {
	// essentially each statefile is just a file a name of which represents a ProjectExperimentState
	// read the file and unmarshal it into ProjectExperimentState
	file, err := os.Open(r.defaultRestartPath + stateFile)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to open state file %s", stateFile)
	}
	defer file.Close()

	var state *ProjectExperimentState

	stateDesc := strings.Split(stateFile, ".")
	if len(stateDesc) != 3 {
		return nil, errors.Errorf("invalid state file name %s", stateFile)
	}

	if !isValidState(stateDesc[2]) {
		return nil, errors.Errorf("invalid state %s", stateDesc[2])
	}

  // read runArgs from the file
  runArgs := RunArgs{}
  if err := json.NewDecoder(file).Decode(&runArgs); err != nil {
    return nil, errors.WithMessagef(err, "failed to decode runArgs")
  }

	state = &ProjectExperimentState{
		ProjectName:    stateDesc[0],
		ExperimentName: stateDesc[1],
		State:          State(stateDesc[2]),
    RunArgs:        runArgs,
	}

	return state, nil
}

func (r *InnerStateManager) isStateFile(filename string) bool {
	// file should be names as projectName.experimentName.state
	return len(strings.Split(filename, ".")) == 3
}

func (r *InnerStateManager) FillStates() error {
	files, err := os.ReadDir(r.defaultRestartPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to read restart directory")
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if !r.isStateFile(file.Name()) {
			continue
		}

		state, err := r.readState(file.Name())
		if err != nil {
			return err
		}
		r.States[state.NameAsType()] = *state
	}

	return nil
}

func (r *InnerStateManager) GetState(projectName, experimentName string) (State, error) {
	for _, state := range r.States {
		if state.ProjectName == projectName && state.ExperimentName == experimentName {
			return state.State, nil
		}
	}
	return "", errors.Errorf("state for project %s experiment %s not found", projectName, experimentName)
}

func (r *InnerStateManager) SetState(
	projectName,
	experimentName string,
	state State,
	runArgs RunArgs,
) error {
	newState := ProjectExperimentState{
		ProjectName:    projectName,
		ExperimentName: experimentName,
		State:          state,
    RunArgs:        runArgs,
	}

	r.States[newState.NameAsType()] = newState

	return nil
}

func (r *InnerStateManager) UpdateStates() error {
	// empty the directory
	files, err := os.ReadDir(r.defaultRestartPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to read restart directory")
	}
	for _, file := range files {
		if file.IsDir() && !r.isStateFile(file.Name()) {
			continue
		}

		if err := os.Remove(r.defaultRestartPath + file.Name()); err != nil {
			return errors.WithMessagef(err, "failed to remove file %s", file.Name())
		}
	}

	for _, state := range r.States {
   if err := state.Write(r.defaultRestartPath); err != nil {
      return errors.WithMessagef(err, "failed to write state")
    }
	}

	return nil
}
