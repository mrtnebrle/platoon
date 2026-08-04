package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

type DagrStatus string

const (
	DagrPending DagrStatus = "pending"
	DagrReady   DagrStatus = "ready"
	DagrRunning DagrStatus = "running"
	DagrWaiting DagrStatus = "waiting"
	DagrDone    DagrStatus = "done"
	DagrFailed  DagrStatus = "failed"
	DagrSkipped DagrStatus = "skipped"
)

type DagrRun struct {
	RunID    string
	StageIDs map[string]string
}

type DagrRecoveryState string

const (
	DagrRunAbsent    DagrRecoveryState = "absent"
	DagrRunFound     DagrRecoveryState = "found"
	DagrRunAmbiguous DagrRecoveryState = "ambiguous"
)

type DagrRecovery struct {
	State DagrRecoveryState
	RunID string
}

type Dagr interface {
	LoadWorkflow(context.Context, string, string, []string) error
	ListStages(context.Context, string, []string) (map[string]string, error)
	StartRun(context.Context, string) (string, error)
	RecoverRun(context.Context, string) (DagrRecovery, error)
	Snapshot(context.Context, string, []string) (map[string]DagrStatus, error)
	SetTerminal(context.Context, string, string, bool) error
}

type DagrCLI struct {
	config    manifest.DagrAdapter
	executor  Executor
	timeout   time.Duration
	maxOutput int
}

func NewDagrCLI(config manifest.DagrAdapter, executor Executor, timeout time.Duration, maxOutput int) *DagrCLI {
	return &DagrCLI{config: config, executor: executor, timeout: timeout, maxOutput: maxOutput}
}

func WorkflowYAML(name string, stages []manifest.Stage) ([]byte, error) {
	if !workflowNamePattern.MatchString(name) {
		return nil, errors.New("dagr workflow name is invalid")
	}
	ordered := append([]manifest.Stage(nil), stages...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	var output bytes.Buffer
	fmt.Fprintf(&output, "name: %s\nstages:\n", name)
	for index, stage := range ordered {
		if !stageNamePattern.MatchString(stage.ID) {
			return nil, fmt.Errorf("dagr stage name %q is invalid", stage.ID)
		}
		fmt.Fprintf(&output, "  - name: %s\n", stage.ID)
		dependencies := append([]string(nil), stage.DependsOn...)
		sort.Strings(dependencies)
		if len(dependencies) > 0 {
			fmt.Fprintln(&output, "    depends-on:")
			for _, dependency := range dependencies {
				fmt.Fprintf(&output, "      - %s\n", dependency)
			}
		}
		fmt.Fprintf(&output, "    position: %d\n", (index+1)*10)
	}
	return output.Bytes(), nil
}

func (d *DagrCLI) Start(ctx context.Context, workflowFile, workflowName string, stageNames []string) (DagrRun, error) {
	stageIDs, err := d.Prepare(ctx, workflowFile, workflowName, stageNames)
	if err != nil {
		return DagrRun{}, err
	}
	runID, err := d.StartRun(ctx, workflowName)
	if err != nil {
		return DagrRun{}, err
	}
	return DagrRun{RunID: runID, StageIDs: stageIDs}, nil
}

func (d *DagrCLI) Prepare(ctx context.Context, workflowFile, workflowName string, stageNames []string) (map[string]string, error) {
	if err := d.LoadWorkflow(ctx, workflowFile, workflowName, stageNames); err != nil {
		return nil, err
	}
	return d.ListStages(ctx, workflowName, stageNames)
}

func (d *DagrCLI) LoadWorkflow(ctx context.Context, workflowFile, workflowName string, stageNames []string) error {
	load, err := d.run(ctx, "workflow", "load", workflowFile)
	if err != nil {
		return fmt.Errorf("dagr workflow load failed: %w", err)
	}
	if err := parseWorkflowLoad(load, workflowName, stageNames); err != nil {
		return err
	}
	return nil
}

func (d *DagrCLI) ListStages(ctx context.Context, workflowName string, stageNames []string) (map[string]string, error) {
	listed, err := d.run(ctx, "stage", "list", workflowName)
	if err != nil {
		return nil, fmt.Errorf("dagr stage listing failed: %w", err)
	}
	stageIDs, err := parseStageList(listed, stageNames)
	if err != nil {
		return nil, err
	}
	return stageIDs, nil
}

func (d *DagrCLI) StartRun(ctx context.Context, workflowName string) (string, error) {
	started, err := d.run(ctx, "run", "start", workflowName)
	if err != nil {
		return "", fmt.Errorf("dagr run start failed after workflow publication: %w", err)
	}
	runID, err := parseRunStart(started, workflowName)
	if err != nil {
		return "", err
	}
	return runID, nil
}

func (d *DagrCLI) RecoverRun(ctx context.Context, workflowName string) (DagrRecovery, error) {
	if !workflowNamePattern.MatchString(workflowName) {
		return DagrRecovery{}, errors.New("dagr workflow name is invalid")
	}
	query := fmt.Sprintf("SELECT r.id FROM runs AS r JOIN dags AS d ON d.id = r.dag_id WHERE d.name = '%s' ORDER BY r.created_at;", workflowName)
	result, err := d.executor.Run(ctx, Invocation{
		Executable: d.config.InspectExecutable,
		Args:       []string{"-readonly", "-batch", "-noheader", d.config.Database, query},
		Timeout:    d.timeout,
		MaxOutput:  d.maxOutput,
	})
	if err != nil {
		return DagrRecovery{}, fmt.Errorf("dagr run recovery inspection failed: %w", err)
	}
	if len(result.Stderr) != 0 {
		return DagrRecovery{}, errors.New("dagr run recovery inspection wrote unexpected diagnostics")
	}
	lines := outputLines(result.Stdout)
	if len(lines) == 0 {
		return DagrRecovery{State: DagrRunAbsent}, nil
	}
	if len(lines) > 1 {
		return DagrRecovery{State: DagrRunAmbiguous}, nil
	}
	if !uuidPattern.MatchString(lines[0]) {
		return DagrRecovery{}, errors.New("dagr run recovery returned an invalid identity")
	}
	return DagrRecovery{State: DagrRunFound, RunID: lines[0]}, nil
}

func (d *DagrCLI) Snapshot(ctx context.Context, runID string, stageNames []string) (map[string]DagrStatus, error) {
	if !uuidPattern.MatchString(runID) {
		return nil, errors.New("dagr run ID is invalid")
	}
	result, err := d.run(ctx, "run", "watch", runID)
	if err != nil {
		return nil, fmt.Errorf("dagr snapshot failed: %w", err)
	}
	return parseSnapshot(result, runID, stageNames)
}

func (d *DagrCLI) SetTerminal(ctx context.Context, runID, stageID string, success bool) error {
	if !uuidPattern.MatchString(runID) || !uuidPattern.MatchString(stageID) {
		return errors.New("dagr terminal transition identifiers are invalid")
	}
	action := "step-fail"
	prefix := "step failed"
	if success {
		action = "step-done"
		prefix = "step done"
	}
	result, err := d.run(ctx, "run", action, runID, stageID)
	if err != nil {
		return fmt.Errorf("dagr terminal transition failed: %w", err)
	}
	want := fmt.Sprintf("%s: run=%s stage=%s\n", prefix, runID, stageID)
	if string(result.Stdout) != want {
		return errors.New("dagr terminal acknowledgement was malformed")
	}
	return nil
}

func (d *DagrCLI) run(ctx context.Context, args ...string) (Result, error) {
	allArgs := append([]string(nil), d.config.Args...)
	allArgs = append(allArgs, "--db", d.config.Database)
	allArgs = append(allArgs, args...)
	result, err := d.executor.Run(ctx, Invocation{
		Executable: d.config.Executable,
		Args:       allArgs,
		Timeout:    d.timeout,
		MaxOutput:  d.maxOutput,
	})
	if err != nil {
		return Result{}, err
	}
	if len(result.Stderr) != 0 {
		return Result{}, errors.New("dagr wrote unexpected diagnostic output")
	}
	return result, nil
}

func parseWorkflowLoad(result Result, name string, stages []string) error {
	lines := outputLines(result.Stdout)
	if len(lines) < len(stages)+2 {
		return errors.New("dagr workflow load receipt was incomplete")
	}
	created := regexp.MustCompile(`^(?:created dag|using existing dag) "` + regexp.QuoteMeta(name) + `" \(([0-9a-f-]+)\)$`)
	match := created.FindStringSubmatch(lines[0])
	if len(match) != 2 || !uuidPattern.MatchString(match[1]) {
		return errors.New("dagr workflow identity receipt was malformed")
	}
	want := make(map[string]bool, len(stages))
	for _, stage := range stages {
		want[stage] = false
	}
	for _, line := range lines[1 : len(lines)-1] {
		trimmed := strings.TrimSpace(line)
		matched := ""
		for stage := range want {
			addedPrefix := "added stage \"" + stage + "\" ("
			existing := "stage \"" + stage + "\" already exists, skipping"
			if strings.HasPrefix(trimmed, addedPrefix) && strings.HasSuffix(trimmed, ")") {
				id := strings.TrimSuffix(strings.TrimPrefix(trimmed, addedPrefix), ")")
				if uuidPattern.MatchString(id) {
					matched = stage
				}
			} else if trimmed == existing {
				matched = stage
			}
		}
		if matched == "" || want[matched] {
			return errors.New("dagr workflow stage receipt was malformed")
		}
		want[matched] = true
	}
	for _, seen := range want {
		if !seen {
			return errors.New("dagr workflow stage receipt was incomplete")
		}
	}
	if lines[len(lines)-1] != "loaded workflow \""+name+"\"" {
		return errors.New("dagr workflow completion receipt was malformed")
	}
	return nil
}

func parseStageList(result Result, expected []string) (map[string]string, error) {
	lines := outputLines(result.Stdout)
	if len(lines) != len(expected)+1 {
		return nil, errors.New("dagr stage list was malformed")
	}
	header := strings.Fields(lines[0])
	if len(header) < 2 || header[0] != "ID" || header[1] != "NAME" {
		return nil, errors.New("dagr stage list was malformed")
	}
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		want[name] = true
	}
	resultIDs := make(map[string]string, len(expected))
	seenIDs := map[string]bool{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 2 || !uuidPattern.MatchString(fields[0]) || !want[fields[1]] || seenIDs[fields[0]] {
			return nil, errors.New("dagr stage list row was malformed")
		}
		if _, duplicate := resultIDs[fields[1]]; duplicate {
			return nil, errors.New("dagr stage list contained duplicate names")
		}
		seenIDs[fields[0]] = true
		resultIDs[fields[1]] = fields[0]
	}
	if len(resultIDs) != len(expected) {
		return nil, errors.New("dagr stage list was incomplete")
	}
	return resultIDs, nil
}

func parseRunStart(result Result, workflowName string) (string, error) {
	line := strings.TrimSuffix(string(result.Stdout), "\n")
	prefix := "started run "
	suffix := " for dag \"" + workflowName + "\""
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return "", errors.New("dagr run receipt was malformed")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	if !uuidPattern.MatchString(id) {
		return "", errors.New("dagr run receipt contained an invalid ID")
	}
	return id, nil
}

func parseSnapshot(result Result, runID string, expected []string) (map[string]DagrStatus, error) {
	if !strings.Contains(string(result.Stdout), "run:"+runID[:8]) {
		return nil, errors.New("dagr snapshot did not identify the requested run")
	}
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		want[name] = true
	}
	statuses := make(map[string]DagrStatus, len(expected))
	for _, line := range outputLines(result.Stdout) {
		fields := strings.Fields(line)
		for index, field := range fields {
			status, iconOK := dagrIconStatus[field]
			if !iconOK || index == 0 || index+1 >= len(fields) || string(status) != fields[index+1] {
				continue
			}
			name := fields[index-1]
			if !want[name] {
				return nil, errors.New("dagr snapshot contained an unknown stage")
			}
			if _, duplicate := statuses[name]; duplicate {
				return nil, errors.New("dagr snapshot repeated a stage")
			}
			statuses[name] = status
		}
	}
	if len(statuses) != len(expected) {
		return nil, errors.New("dagr snapshot omitted a stage")
	}
	return statuses, nil
}

func outputLines(output []byte) []string {
	trimmed := strings.TrimSuffix(string(output), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

var (
	uuidPattern         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	workflowNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	stageNamePattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	dagrIconStatus      = map[string]DagrStatus{
		"◌": DagrPending,
		"○": DagrReady,
		"●": DagrRunning,
		"?": DagrWaiting,
		"✓": DagrDone,
		"✗": DagrFailed,
		"⊘": DagrSkipped,
	}
)
