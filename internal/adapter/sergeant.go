package adapter

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

type DispatchRequest struct {
	Project       string
	Task          string
	Repository    string
	Branch        string
	Harness       string
	Stage         string
	IntentFile    string
	OriginProfile string
	CorrelationID string
}

type Dispatcher interface {
	Dispatch(context.Context, DispatchRequest) (string, error)
}

type SergeantCLI struct {
	command   manifest.Command
	executor  Executor
	timeout   time.Duration
	maxOutput int
}

func NewSergeantCLI(command manifest.Command, executor Executor, timeout time.Duration, maxOutput int) *SergeantCLI {
	return &SergeantCLI{command: command, executor: executor, timeout: timeout, maxOutput: maxOutput}
}

func (s *SergeantCLI) Dispatch(ctx context.Context, request DispatchRequest) (string, error) {
	if request.Project == "" || request.Task == "" || request.Repository == "" || request.Branch == "" ||
		request.Harness == "" || request.Stage == "" || request.IntentFile == "" ||
		request.OriginProfile == "" || request.CorrelationID == "" {
		return "", errors.New("Sergeant dispatch request is incomplete")
	}
	args := append([]string(nil), s.command.Args...)
	args = append(args,
		request.Project,
		"--td", request.Task,
		"--repos", request.Repository,
		"--branch", request.Branch,
		"--agent", request.Harness,
		"--stage", request.Stage,
		"--intent-file", request.IntentFile,
		"--origin-profile", request.OriginProfile,
		"--correlation-id", request.CorrelationID,
	)
	result, err := s.executor.Run(ctx, Invocation{
		Executable: s.command.Executable,
		Args:       args,
		Timeout:    s.timeout,
		MaxOutput:  s.maxOutput,
	})
	if err != nil {
		return "", err
	}
	if len(result.Stderr) != 0 {
		return "", errors.New("Sergeant dispatch wrote unexpected diagnostic output")
	}
	return parseDispatchReceipt(result.Stdout)
}

func parseDispatchReceipt(output []byte) (string, error) {
	var early, final []string
	for _, line := range outputLines(output) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "task-id: ") {
			early = append(early, strings.TrimPrefix(trimmed, "task-id: "))
		}
		if strings.HasPrefix(trimmed, "Task ID: ") {
			final = append(final, strings.TrimPrefix(trimmed, "Task ID: "))
		}
	}
	if len(early) != 1 || len(final) != 1 || early[0] != final[0] || !safeOpaqueID(early[0]) {
		return "", errors.New("Sergeant dispatch receipt was missing or ambiguous")
	}
	return early[0], nil
}

func safeOpaqueID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
