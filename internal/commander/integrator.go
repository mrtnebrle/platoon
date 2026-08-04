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
		Executable: "git", Args: []string{"-C", repository.Path, "rev-parse", "HEAD"},
		Timeout: c.Timeout, MaxOutput: c.MaxOutput,
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

func (c CommandIntegrator) ContainsBase(ctx context.Context, worktree, base string) (bool, error) {
	result, err := c.Executor.Run(ctx, adapter.Invocation{
		Executable: "git", Args: []string{"-C", worktree, "merge-base", base, "HEAD"},
		Timeout: c.Timeout, MaxOutput: c.MaxOutput,
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

func (c CommandIntegrator) Run(ctx context.Context, worktree string, commands []manifest.Command) error {
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
	return nil
}
