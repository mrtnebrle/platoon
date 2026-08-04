package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/planner"
)

const (
	maxFleetField  = 64 << 10
	maxFleetScan   = 1024
	callbackSchema = "sergeant.callback-origin/v1"
)

type FleetStatus string

const (
	FleetDispatched FleetStatus = "dispatched"
	FleetInProgress FleetStatus = "in_progress"
	FleetNeedsInput FleetStatus = "needs_input"
	FleetBlocked    FleetStatus = "blocked"
	FleetWaiting    FleetStatus = "waiting"
	FleetOrphaned   FleetStatus = "orphaned"
	FleetDrained    FleetStatus = "drained"
	FleetDone       FleetStatus = "done"
	FleetFailed     FleetStatus = "failed"
)

type FleetBinding struct {
	Project            string
	Task               string
	Stage              string
	Branch             string
	IntentRevision     string
	RequireCorrelation bool
	OriginProfile      string
	CorrelationID      string
}

type FleetEvidence struct {
	FleetID        string
	Repository     string
	Status         FleetStatus
	ResultDigest   string
	Worktree       string
	InitialSHA     string
	IntentRevision string
}

type FleetReader struct {
	root string
}

func NewFleetReader(root string) *FleetReader {
	return &FleetReader{root: root}
}

func (r *FleetReader) Read(fleetID, repository string, binding FleetBinding) (FleetEvidence, error) {
	if !safeOpaqueID(fleetID) || !safeOpaqueID(repository) {
		return FleetEvidence{}, errors.New("fleet identity is invalid")
	}
	taskDir := filepath.Join(r.root, fleetID)
	repoDir := filepath.Join(taskDir, repository)
	for _, directory := range []string{r.root, taskDir, repoDir} {
		if err := requireRealDirectory(directory); err != nil {
			return FleetEvidence{}, fmt.Errorf("fleet evidence directory is invalid: %w", err)
		}
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return FleetEvidence{}, errors.New("fleet repository ownership cannot be read")
	}
	ownedRepositories := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && safeOpaqueID(entry.Name()) {
			ownedRepositories = append(ownedRepositories, entry.Name())
		}
	}
	if len(ownedRepositories) != 1 || ownedRepositories[0] != repository {
		return FleetEvidence{}, errors.New("fleet must own exactly one repository")
	}
	rootRevision, err := readStableText(filepath.Join(taskDir, "intent_revision"), 256, true)
	if err != nil {
		return FleetEvidence{}, errors.New("fleet intent evidence is unavailable")
	}
	repoRevision, err := readStableText(filepath.Join(repoDir, "intent_revision"), 256, true)
	if err != nil {
		return FleetEvidence{}, errors.New("repository intent evidence is unavailable")
	}
	if binding.IntentRevision == "" || rootRevision != binding.IntentRevision || repoRevision != binding.IntentRevision {
		return FleetEvidence{}, errors.New("fleet intent revision does not match")
	}
	if binding.RequireCorrelation {
		origin, err := readCallbackOrigin(taskDir)
		if err != nil || origin.Profile != binding.OriginProfile || origin.CorrelationID != binding.CorrelationID {
			return FleetEvidence{}, errors.New("fleet dispatch correlation does not match")
		}
	}
	checks := []struct {
		name string
		want string
	}{
		{"project", binding.Project},
		{"td_task", binding.Task},
		{"stage", binding.Stage},
		{"branch", binding.Branch},
	}
	for _, check := range checks {
		value, err := readStableText(filepath.Join(repoDir, check.name), 4096, true)
		if err != nil || value != check.want {
			return FleetEvidence{}, fmt.Errorf("fleet %s binding does not match", check.name)
		}
	}
	rawStatus, err := readStableText(filepath.Join(repoDir, "status"), 4096, true)
	if err != nil {
		return FleetEvidence{}, errors.New("fleet status evidence is unavailable")
	}
	status, err := parseFleetStatus(rawStatus)
	if err != nil {
		return FleetEvidence{}, err
	}
	worktree, err := readStableText(filepath.Join(repoDir, "worktree"), maxFleetField, true)
	if err != nil {
		return FleetEvidence{}, errors.New("fleet worktree evidence is unavailable")
	}
	if err := requireRealDirectory(worktree); err != nil {
		return FleetEvidence{}, errors.New("fleet worktree is not a real directory")
	}
	initialSHA, err := readStableText(filepath.Join(repoDir, "initial_sha"), 256, true)
	if err != nil || !validGitHash(initialSHA) {
		return FleetEvidence{}, errors.New("fleet dispatch base evidence is invalid")
	}
	evidence := FleetEvidence{
		FleetID: fleetID, Repository: repository, Status: status, Worktree: worktree,
		InitialSHA: initialSHA, IntentRevision: binding.IntentRevision,
	}
	if status == FleetDone {
		result, err := readStableBytes(filepath.Join(repoDir, "result"), maxFleetField)
		if err != nil || len(bytes.TrimSpace(result)) == 0 {
			return FleetEvidence{}, errors.New("done fleet lacks verified result evidence")
		}
		sum := sha256.Sum256(result)
		evidence.ResultDigest = hex.EncodeToString(sum[:])
	}
	return evidence, nil
}

func (r *FleetReader) FindByCorrelation(profile, correlationID string) ([]string, error) {
	if !safeOpaqueID(profile) || !safeOpaqueID(correlationID) {
		return nil, errors.New("callback correlation is invalid")
	}
	if err := requireRealDirectory(r.root); err != nil {
		return nil, errors.New("fleet root is unavailable")
	}
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil, errors.New("fleet root cannot be scanned")
	}
	if len(entries) > maxFleetScan {
		return nil, fmt.Errorf("fleet scan exceeds %d entries", maxFleetScan)
	}
	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() || !safeOpaqueID(entry.Name()) {
			continue
		}
		taskDir := filepath.Join(r.root, entry.Name())
		callbackDir := filepath.Join(taskDir, ".callbacks")
		originPath := filepath.Join(callbackDir, "origin.json")
		if _, err := os.Lstat(originPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, errors.New("callback origin evidence cannot be inspected")
		}
		if err := requireRealDirectory(taskDir); err != nil {
			return nil, errors.New("callback task directory is invalid")
		}
		if err := requireRealDirectory(callbackDir); err != nil {
			return nil, errors.New("callback directory is invalid")
		}
		origin, err := readCallbackOrigin(taskDir)
		if err != nil {
			return nil, errors.New("callback origin evidence is malformed")
		}
		if origin.Version != callbackSchema || !safeOpaqueID(origin.Profile) || !safeOpaqueID(origin.CorrelationID) {
			return nil, errors.New("callback origin evidence is invalid")
		}
		if origin.Profile == profile && origin.CorrelationID == correlationID {
			matches = append(matches, entry.Name())
		}
	}
	sort.Strings(matches)
	return matches, nil
}

func readCallbackOrigin(taskDir string) (callbackOrigin, error) {
	callbackDir := filepath.Join(taskDir, ".callbacks")
	if err := requireRealDirectory(taskDir); err != nil {
		return callbackOrigin{}, err
	}
	if err := requireRealDirectory(callbackDir); err != nil {
		return callbackOrigin{}, err
	}
	var origin callbackOrigin
	if err := readStrictJSON(filepath.Join(callbackDir, "origin.json"), &origin, 4096); err != nil {
		return callbackOrigin{}, err
	}
	if origin.Version != callbackSchema || !safeOpaqueID(origin.Profile) || !safeOpaqueID(origin.CorrelationID) {
		return callbackOrigin{}, errors.New("callback origin evidence is invalid")
	}
	return origin, nil
}

type callbackOrigin struct {
	Version       string `json:"version"`
	Profile       string `json:"profile"`
	CorrelationID string `json:"correlation_id"`
}

type ChangedPath struct {
	Path    string
	Symlink bool
}

type PathViolation struct {
	Path   string
	Reason string
}

type GitInspector struct {
	executor  Executor
	timeout   time.Duration
	maxOutput int
}

func NewGitInspector(executor Executor, timeout time.Duration, maxOutput int) *GitInspector {
	return &GitInspector{executor: executor, timeout: timeout, maxOutput: maxOutput}
}

func (g *GitInspector) ChangedPaths(ctx context.Context, worktree, initialSHA string) ([]ChangedPath, error) {
	before, statErr := os.Lstat(worktree)
	if statErr != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || !validGitHash(initialSHA) {
		return nil, errors.New("changed-path evidence inputs are invalid")
	}
	tracked, err := g.git(ctx, "-C", worktree, "diff", "--name-only", "-z", "--no-renames", initialSHA, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := g.git(ctx, "-C", worktree, "ls-files", "--others", "-z")
	if err != nil {
		return nil, err
	}
	rawModes, err := g.git(ctx, "-C", worktree, "diff", "--raw", "-z", "--no-renames", initialSHA, "--")
	if err != nil {
		return nil, err
	}
	symlinkPaths, err := parseRawSymlinkPaths(rawModes)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(worktree)
	if err != nil || !os.SameFile(before, after) || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("worktree identity changed during diff inspection")
	}
	paths, err := parseNULPaths(append(tracked, untracked...))
	if err != nil {
		return nil, err
	}
	unique := make(map[string]ChangedPath, len(paths))
	for _, changed := range paths {
		if isSergeantControlPath(changed) {
			continue
		}
		if err := manifest.ValidateClaimPath(changed); err != nil {
			return nil, errors.New("Git reported an unsafe changed path")
		}
		item := ChangedPath{Path: changed, Symlink: symlinkPaths[changed]}
		info, err := os.Lstat(filepath.Join(worktree, filepath.FromSlash(changed)))
		if err == nil {
			item.Symlink = item.Symlink || info.Mode()&os.ModeSymlink != 0
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("changed path cannot be inspected safely")
		}
		unique[changed] = item
	}
	result := make([]ChangedPath, 0, len(unique))
	for _, item := range unique {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func isSergeantControlPath(value string) bool {
	if strings.Contains(value, "/") {
		for _, directory := range []string{
			".sergeant-notification-acks/", ".sergeant-notification-accepts/", ".sergeant-notification-complete/",
		} {
			if strings.HasPrefix(value, directory) {
				return true
			}
		}
		return false
	}
	_, ok := sergeantControlFiles[value]
	return ok
}

var sergeantControlFiles = map[string]struct{}{
	".sergeant-brief.md":            {},
	".sergeant-drain-signal":        {},
	".sergeant-drained":             {},
	".sergeant-gate-generation":     {},
	".sergeant-intent.md":           {},
	".sergeant-message":             {},
	".sergeant-notification":        {},
	".sergeant-response":            {},
	".sergeant-response-applied":    {},
	".sergeant-response-generation": {},
	".sergeant-response-id":         {},
	".sergeant-result":              {},
	".sergeant-status":              {},
	".sergeant-validation-ready":    {},
	".sergeant-wake-condition":      {},
}

func (g *GitInspector) git(ctx context.Context, args ...string) ([]byte, error) {
	result, err := g.executor.Run(ctx, Invocation{Executable: "git", Args: args, Timeout: g.timeout, MaxOutput: g.maxOutput})
	if err != nil {
		return nil, fmt.Errorf("Git evidence command failed: %w", err)
	}
	if len(result.Stderr) != 0 {
		return nil, errors.New("Git evidence command wrote unexpected diagnostics")
	}
	return result.Stdout, nil
}

func CheckPathClaims(changed []ChangedPath, claims []string) []PathViolation {
	var violations []PathViolation
	for _, item := range changed {
		if item.Symlink {
			violations = append(violations, PathViolation{Path: sanitizePath(item.Path), Reason: "changed symlink is not claim-safe"})
			continue
		}
		covered := false
		for _, claim := range claims {
			if planner.CoversPath(claim, item.Path) {
				covered = true
				break
			}
		}
		if !covered {
			violations = append(violations, PathViolation{Path: sanitizePath(item.Path), Reason: "path is outside declared claims"})
		}
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Path < violations[j].Path })
	return violations
}

func parseFleetStatus(value string) (FleetStatus, error) {
	switch value {
	case "dispatched":
		return FleetDispatched, nil
	case "in_progress":
		return FleetInProgress, nil
	case "needs_input":
		return FleetNeedsInput, nil
	case "blocked":
		return FleetBlocked, nil
	case "waiting":
		return FleetWaiting, nil
	case "orphaned":
		return FleetOrphaned, nil
	case "drained":
		return FleetDrained, nil
	case "done":
		return FleetDone, nil
	default:
		if strings.HasPrefix(value, "failed: ") && strings.TrimSpace(strings.TrimPrefix(value, "failed: ")) != "" {
			return FleetFailed, nil
		}
		return "", errors.New("fleet status is not a recognized durable state")
	}
}

func readStableText(path string, limit int64, required bool) (string, error) {
	raw, err := readStableBytes(path, limit)
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(raw), "\n")
	if strings.ContainsAny(value, "\x00\r\n") || (required && value == "") {
		return "", errors.New("fleet field is empty or multiline")
	}
	return value, nil
}

func readStableBytes(path string, limit int64) ([]byte, error) {
	first, err := readBoundedFile(path, limit)
	if err != nil {
		return nil, err
	}
	second, err := readBoundedFile(path, limit)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, second) {
		return nil, errors.New("fleet evidence changed while reading")
	}
	return first, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("fleet field is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("fleet field changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("fleet field exceeds its read limit")
	}
	return raw, nil
}

func readStrictJSON(path string, destination any, limit int64) error {
	raw, err := readStableBytes(path, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON evidence contains trailing data")
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a real directory")
	}
	return nil
}

func parseNULPaths(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, errors.New("Git path output was truncated")
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 || !bytes.Equal([]byte(string(part)), part) {
			return nil, errors.New("Git path output was malformed")
		}
		result = append(result, string(part))
	}
	return result, nil
}

func parseRawSymlinkPaths(raw []byte) (map[string]bool, error) {
	result := map[string]bool{}
	if len(raw) == 0 {
		return result, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, errors.New("Git raw diff output was truncated")
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(parts)%2 != 0 {
		return nil, errors.New("Git raw diff output was malformed")
	}
	for index := 0; index < len(parts); index += 2 {
		fields := strings.Fields(string(parts[index]))
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || len(parts[index+1]) == 0 {
			return nil, errors.New("Git raw diff row was malformed")
		}
		path := string(parts[index+1])
		if strings.TrimPrefix(fields[0], ":") == "120000" || fields[1] == "120000" {
			result[path] = true
		}
	}
	return result, nil
}

func validGitHash(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sanitizePath(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if builder.Len() >= 256 {
			break
		}
		if r < 0x20 || r == 0x7f {
			builder.WriteByte('?')
		} else {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
