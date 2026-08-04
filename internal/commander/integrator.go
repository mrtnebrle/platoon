package commander

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/adapter"
	"github.com/mrtnebrle/platoon/internal/manifest"
)

type CommandIntegrator struct {
	Executor  adapter.Executor
	Timeout   time.Duration
	MaxOutput int
}

func (c CommandIntegrator) Head(ctx context.Context, repository manifest.Repository) (string, error) {
	result, err := c.Executor.Run(ctx, adapter.Invocation{
		Executable: "git", Args: []string{"--no-replace-objects", "-C", repository.Path, "rev-parse", "HEAD"},
		Timeout: c.Timeout, MaxOutput: c.MaxOutput, Env: controlledGitEnvironment(),
	})
	if err != nil || len(result.Stderr) != 0 {
		return "", errors.New("repository base command failed")
	}
	head := strings.TrimSuffix(string(result.Stdout), "\n")
	if len(head) != 40 && len(head) != 64 {
		return "", errors.New("repository base command returned invalid identity")
	}
	if _, err := hex.DecodeString(head); err != nil {
		return "", errors.New("repository base command returned invalid identity")
	}
	return head, nil
}

func (c CommandIntegrator) ContainsBase(ctx context.Context, worktree, gitDir, base string) (bool, error) {
	result, err := c.Executor.Run(ctx, adapter.Invocation{
		Executable: "git", Args: []string{"--no-replace-objects", "--git-dir=" + gitDir, "--work-tree=" + worktree, "merge-base", base, "HEAD"},
		Timeout: c.Timeout, MaxOutput: c.MaxOutput, Env: controlledGitEnvironment(),
	})
	if err != nil || len(result.Stderr) != 0 {
		return false, errors.New("candidate ancestry command failed")
	}
	ancestor := strings.TrimSuffix(string(result.Stdout), "\n")
	if len(ancestor) != 40 && len(ancestor) != 64 {
		return false, errors.New("candidate ancestry command returned invalid identity")
	}
	if _, err := hex.DecodeString(ancestor); err != nil {
		return false, errors.New("candidate ancestry command returned invalid identity")
	}
	return ancestor == base, nil
}

func (c CommandIntegrator) Run(ctx context.Context, worktree, gitPointer, gitDir string, commands []manifest.Command) error {
	if err := adapter.VerifyWorktreeGitIdentity(worktree, gitPointer, gitDir); err != nil {
		return err
	}
	before, err := os.Lstat(worktree)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("integration worktree is not a stable real directory")
	}
	for _, command := range commands {
		result, err := c.Executor.Run(ctx, adapter.Invocation{
			Executable: command.Executable, Args: append([]string(nil), command.Args...), Directory: worktree,
			Timeout: c.Timeout, MaxOutput: c.MaxOutput,
		})
		if err != nil || len(result.Stderr) != 0 {
			return errors.New("integration command failed")
		}
	}
	after, err := os.Lstat(worktree)
	if err != nil || !os.SameFile(before, after) || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 {
		return errors.New("integration worktree identity changed")
	}
	if err := adapter.VerifyWorktreeGitIdentity(worktree, gitPointer, gitDir); err != nil {
		return err
	}
	return nil
}

func controlledGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+5)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_OPTIONAL_LOCKS=0",
	)
}
