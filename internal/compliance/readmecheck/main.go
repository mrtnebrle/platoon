package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(".", os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "README reconciliation:", err)
		os.Exit(1)
	}
}

func run(repository string, getenv func(string) string) error {
	head := getenv("PLATOON_HEAD_SHA")
	if head == "" {
		head = "HEAD"
	}
	base, err := comparisonBase(repository, head, getenv)
	if err != nil {
		return err
	}
	paths, err := changedPaths(repository, base, head)
	if err != nil {
		return err
	}
	visible := false
	for _, path := range paths {
		if operatorVisible(path) {
			visible = true
			break
		}
	}
	if !visible {
		return nil
	}
	changed, err := readmeBlobChanged(repository, base, head)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("operator-visible changes require a README.md content update")
	}
	return nil
}

func comparisonBase(repository, head string, getenv func(string) string) (string, error) {
	switch getenv("PLATOON_EVENT_NAME") {
	case "pull_request":
		base := getenv("PLATOON_PR_BASE_SHA")
		if base == "" {
			return "", errors.New("pull request base SHA is missing")
		}
		return gitOutput(repository, "merge-base", base, head)
	case "push":
		before := getenv("PLATOON_BEFORE_SHA")
		if before != "" && strings.Trim(before, "0") != "" && gitObjectExists(repository, before) {
			return before, nil
		}
		defaultBranch := getenv("PLATOON_DEFAULT_BRANCH")
		if defaultBranch == "" {
			return "", nil
		}
		remote := "origin/" + defaultBranch
		if !gitObjectExists(repository, remote) {
			return "", errors.New("default branch history is unavailable")
		}
		return gitOutput(repository, "merge-base", remote, head)
	default:
		if gitObjectExists(repository, head+"^") {
			return head + "^", nil
		}
		return "", nil
	}
}

func changedPaths(repository, base, head string) ([]string, error) {
	args := []string{"-C", repository}
	if base == "" {
		args = append(args, "ls-tree", "-r", "--name-only", "-z", head)
	} else {
		args = append(args, "diff", "--name-only", "--no-renames", "-z", base, head)
	}
	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, errors.New("Git history required for README reconciliation is unavailable")
	}
	parts := bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, filepath.ToSlash(string(part)))
		}
	}
	return result, nil
}

func operatorVisible(path string) bool {
	path = filepath.ToSlash(path)
	if path == "README.md" || path == "Makefile" || path == "go.mod" || strings.HasPrefix(path, "schema/") || strings.HasPrefix(path, "examples/") || strings.HasPrefix(path, "docs/") {
		return true
	}
	if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, "internal/compliance/") {
		return false
	}
	return (strings.HasPrefix(path, "cmd/") || strings.HasPrefix(path, "internal/")) && strings.HasSuffix(path, ".go")
}

func readmeBlobChanged(repository, base, head string) (bool, error) {
	headBlob, err := gitOutput(repository, "rev-parse", head+":README.md")
	if err != nil {
		return false, errors.New("README.md is missing at head")
	}
	if base == "" {
		return headBlob != "", nil
	}
	baseBlob, err := gitOutput(repository, "rev-parse", base+":README.md")
	if err != nil {
		return true, nil
	}
	return baseBlob != headBlob, nil
}

func gitObjectExists(repository, object string) bool {
	command := exec.Command("git", "-C", repository, "cat-file", "-e", object+"^{object}")
	return command.Run() == nil
}

func gitOutput(repository string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", repository}, args...)
	output, err := exec.Command("git", commandArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
