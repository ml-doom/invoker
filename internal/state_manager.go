package internal

import (
	"context"
	"encoding/json"

	"fmt"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
)

type Host string
type ProjectExperimentStr string

type StateMatch struct {
	Expected State
	Actual   int
	RunArgs  RunArgs
}

// need to be
var badExitCodes = mapset.NewSet[int]([]int{1, 255}...)
var okExitCodes = mapset.NewSet[int]([]int{0, 137}...)

func (s StateMatch) ShouldRestart() bool {
	return s.Expected == Running && (badExitCodes.Contains(s.Actual) || !okExitCodes.Contains(s.Actual))
}

type ExperimentState map[ProjectExperimentStr]StateMatch

type RetStatePage map[Host]ExperimentState

type ExperimentHostPageToRestart map[ProjectExperimentStr]RunArgs

type StateManager struct {
	ctx context.Context
	// invoker is invoked only within one project usually, so have a project supplied
	projectName string
	curIP       Host
	page        RetStatePage
	ism         *InnerStateManager
	toRestart   ExperimentHostPageToRestart
}

func NewStateManager(ctx context.Context) (*StateManager, error) {
	ip, err := myPublicIP()
	if err != nil {
		return nil, errors.WithMessage(err, "failed to get public IP")
	}

	page := make(RetStatePage)
	page[Host(ip)] = make(ExperimentState)

	ism, err := NewInnerStateManagerWithDefPath()
	if err != nil {
		return nil, errors.WithMessage(err, "failed to create InnerStateManager")
	}

	toRestart := make(ExperimentHostPageToRestart)

	return &StateManager{
		ctx:       ctx,
		curIP:     Host(ip),
		page:      page,
		ism:       ism,
		toRestart: toRestart,
	}, nil
}

func (s *StateManager) FindLocalMatch() (string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", errors.WithMessage(err, "failed to create docker client")
	}
	defer cli.Close()

	if err := s.ism.FillStates(); err != nil {
		return "", errors.WithMessage(err, "failed to fill inner states")
	}

	for experiment := range s.ism.States {
		rargs := s.ism.States[experiment].RunArgs
		// check if page ip is in rargs.Hosts
		// if it is, then check if rargs.Restartable() is Running

		hostSet := mapset.NewSet[string](rargs.Hosts...)
		if hostSet.Contains(string(s.curIP)) {
			expected := rargs.Restartable()
			_, exitCode, err := containerStateAndExitCode(s.ctx, cli, string(experiment))
			if err != nil && !errors.Is(err, ErrContainerNotFound) {
				return "", errors.WithMessage(err, "failed to get container state and exit code")
			}

			actual := exitCode
			s.page[s.curIP][experiment] = StateMatch{
				Expected: expected,
				Actual:   actual,
				RunArgs:  rargs,
			}
		}
	}

	res, err := json.Marshal(s.page)
	if err != nil {
		return "", errors.WithMessage(err, "failed to marshal page")
	}

	return string(res), nil
}

func (s *StateManager) JoinLocalMatches(matches ...string) error {
	// since every string is a local match, we can unmarshall into json, and join them into one.
	for _, match := range matches {
		var page RetStatePage
		if err := json.Unmarshal([]byte(match), &page); err != nil {
			return errors.WithMessage(err, "failed to unmarshal page")
		}
		for host, expState := range page {
			if _, ok := s.page[host]; !ok {
				s.page[host] = make(ExperimentState)
			}
			for experiment, stateMatch := range expState {
				s.page[host][experiment] = stateMatch
			}
		}
	}

	// group the hosts by experiments
	grouped := make(map[ProjectExperimentStr]map[Host]StateMatch)

	for host, stateMatch := range s.page {
		for experiment, match := range stateMatch {
			if _, ok := grouped[experiment]; !ok {
				grouped[experiment] = make(map[Host]StateMatch)
			}
			grouped[experiment][host] = match
		}
	}

	// check whether the RunArgs are the same
	// for all hosts within an experiment, if they should be running
	// FIXME: it wouldn't work if hosts have been changed (e.g. from 8 -> 2)
	// need a centralized server with states in some decent memory storage.
	for experiment, hostState := range grouped {
		failedHosts := make([]Host, 0)

		// that's strange, why do we need to check if len(hostState) == 0?
		if len(hostState) == 0 {
			continue
		}
		// we need to compare run args, thus get the first one
		prevStateMatch, ok := FirstValue(hostState)
		if !ok {
			return errors.New("failed to get first value, despite the length being greater than 0")
		}

		for host, stateMatch := range hostState {
			if !prevStateMatch.RunArgs.Equal(stateMatch.RunArgs) {
				return errors.New("run args are not equal")
			}
			if shouldRestart := stateMatch.ShouldRestart(); shouldRestart {
				failedHosts = append(failedHosts, host)
			}
		}

		// print failed hosts for this experiment
		if len(failedHosts) > 0 {
			fmt.Printf("failed hosts for experiment %s: %v\n", experiment, failedHosts)
			// we assign all hosts to restart
			s.toRestart[experiment] = prevStateMatch.RunArgs
		}
	}

	return nil
}
