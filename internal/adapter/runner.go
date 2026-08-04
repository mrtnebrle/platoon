package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	ErrOutputLimit = errors.New("command output exceeded limit")
	ErrTimeout     = errors.New("command timed out")
)

type Invocation struct {
	Executable string
	Args       []string
	Directory  string
	Timeout    time.Duration
	MaxOutput  int
	Env        []string
}

type Result struct {
	Stdout []byte
	Stderr []byte
}

type Executor interface {
	Run(context.Context, Invocation) (Result, error)
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, invocation Invocation) (Result, error) {
	if invocation.Executable == "" || invocation.Timeout <= 0 || invocation.MaxOutput < 1 {
		return Result{}, errors.New("invalid command invocation")
	}
	commandContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, invocation.Executable, invocation.Args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = time.Second
	command.Dir = invocation.Directory
	if invocation.Env == nil {
		command.Env = os.Environ()
	} else {
		command.Env = append([]string(nil), invocation.Env...)
	}
	stdout := &limitedBuffer{limit: invocation.MaxOutput, onOverflow: cancel}
	stderr := &limitedBuffer{limit: invocation.MaxOutput, onOverflow: cancel}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.overflowed() || stderr.overflowed() {
		return Result{}, ErrOutputLimit
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return Result{}, ErrTimeout
	}
	if commandContext.Err() != nil {
		return Result{}, errors.New("command canceled")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return Result{}, fmt.Errorf("command %q exited with status %d", filepath.Base(invocation.Executable), exitError.ExitCode())
		}
		return Result{}, fmt.Errorf("command %q could not execute", filepath.Base(invocation.Executable))
	}
	return Result{Stdout: stdout.bytes(), Stderr: stderr.bytes()}, nil
}

type limitedBuffer struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	limit      int
	overflow   bool
	onOverflow func()
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.markOverflow()
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.markOverflow()
		return len(data), nil
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

func (b *limitedBuffer) markOverflow() {
	if b.overflow {
		return
	}
	b.overflow = true
	if b.onOverflow != nil {
		b.onOverflow()
	}
}

func (b *limitedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func (b *limitedBuffer) overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}
