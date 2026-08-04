package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOSExecutorUsesLiteralArgumentsAndBoundsOutput(t *testing.T) {
	t.Setenv("PLATOON_HELPER_PROCESS", "arguments")
	result, err := (OSExecutor{}).Run(context.Background(), Invocation{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestAdapterHelperProcess", "--", "literal; touch should-not-exist", "$(false)"},
		Timeout:    10 * time.Second,
		MaxOutput:  1024,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var got []string
	if err := json.Unmarshal(result.Stdout, &got); err != nil {
		t.Fatalf("decode helper output: %v (%q)", err, result.Stdout)
	}
	want := []string{"literal; touch should-not-exist", "$(false)"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}

	t.Setenv("PLATOON_HELPER_PROCESS", "overflow")
	_, err = (OSExecutor{}).Run(context.Background(), Invocation{
		Executable: os.Args[0], Args: []string{"-test.run=TestAdapterHelperProcess"}, Timeout: 10 * time.Second, MaxOutput: 32,
	})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("overflow error = %v, want ErrOutputLimit", err)
	}
}

func TestOSExecutorDoesNotLeakFailedCommandOutput(t *testing.T) {
	t.Setenv("PLATOON_HELPER_PROCESS", "fail")
	result, err := (OSExecutor{}).Run(context.Background(), Invocation{
		Executable: os.Args[0], Args: []string{"-test.run=TestAdapterHelperProcess"}, Timeout: 10 * time.Second, MaxOutput: 1024,
	})
	if err == nil {
		t.Fatal("Run() succeeded")
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("failed result retained output: %#v", result)
	}
	if strings.Contains(err.Error(), "secret-canary") {
		t.Fatalf("error leaked command output: %v", err)
	}
}

func TestOSExecutorTimeoutKillsDescendantProcessGroup(t *testing.T) {
	t.Setenv("PLATOON_HELPER_PROCESS", "descendant")
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv("PLATOON_DESCENDANT_PID_FILE", pidFile)
	started := time.Now()
	_, err := (OSExecutor{}).Run(context.Background(), Invocation{
		Executable: os.Args[0], Args: []string{"-test.run=TestAdapterHelperProcess"},
		Timeout: 200 * time.Millisecond, MaxOutput: 1024,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run() error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout waited on descendant pipes for %s", elapsed)
	}
	rawPID, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("descendant process %d survived timeout: %v", pid, err)
	}
}

func TestAdapterHelperProcess(t *testing.T) {
	mode := os.Getenv("PLATOON_HELPER_PROCESS")
	if mode == "" {
		return
	}
	switch mode {
	case "arguments":
		separator := 0
		for i, arg := range os.Args {
			if arg == "--" {
				separator = i + 1
				break
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(os.Args[separator:])
	case "overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 4096))
	case "fail":
		_, _ = os.Stderr.WriteString("secret-canary")
		os.Exit(9)
	case "descendant":
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
		if err := os.WriteFile(os.Getenv("PLATOON_DESCENDANT_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(7)
		}
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}
