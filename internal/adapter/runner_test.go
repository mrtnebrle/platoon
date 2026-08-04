package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	}
	os.Exit(0)
}
