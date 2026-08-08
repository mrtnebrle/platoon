package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/adapter"
	"github.com/mrtnebrle/platoon/internal/commander"
	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/missioncontrol"
	"github.com/mrtnebrle/platoon/internal/planner"
	"github.com/mrtnebrle/platoon/internal/state"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "plan":
		return runPlan(args[1:], stdout, stderr)
	case "start":
		return runStart(args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "drain":
		return runDrain(args[1:], true, stdout, stderr)
	case "resume":
		return runDrain(args[1:], false, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "manifest YAML file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "validate: unexpected positional arguments")
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "validate: --file is required")
		return 2
	}
	m, err := manifest.LoadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "validate: %v\n", err)
		return 1
	}
	mission, err := missioncontrol.Compile(m, *file)
	if err != nil {
		fmt.Fprintf(stderr, "validate: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "valid: %s\n", m.Metadata.Name)
	if mission.Mode == manifest.MissionDeclarationV1Alpha1 {
		fmt.Fprintf(stdout, "mission: mode=%s schema=%s class=%s ready=%t\n", mission.Mode, mission.Schema, mission.Class, mission.Ready)
		fmt.Fprintf(stdout, "outputs: %s\n", strings.Join(mission.Outputs, ", "))
		for _, decision := range mission.Sufficiency {
			fmt.Fprintf(stdout, "sufficiency: %s: %s\n", decision.Status, decision.Reason)
		}
	}
	return 0
}

func runPlan(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "manifest YAML file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "plan: unexpected positional arguments")
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "plan: --file is required")
		return 2
	}
	m, err := manifest.LoadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "plan: %v\n", err)
		return 1
	}
	mission, err := missioncontrol.Compile(m, *file)
	if err != nil {
		fmt.Fprintf(stderr, "plan: %v\n", err)
		return 1
	}
	if mission.Mode == manifest.MissionDeclarationV1Alpha1 {
		preview := struct {
			Mission   missioncontrol.Preview `json:"mission"`
			Decisions []planner.Decision     `json:"decisions"`
		}{Mission: mission, Decisions: planner.Plan(m, nil)}
		if err := writeJSON(stdout, preview); err != nil {
			fmt.Fprintf(stderr, "plan: write output: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeJSON(stdout, planner.Plan(m, nil)); err != nil {
		fmt.Fprintf(stderr, "plan: write output: %v\n", err)
		return 1
	}
	return 0
}

func runStart(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "manifest YAML file")
	stateRoot := flags.String("state", ".platoon", "Platoon state root")
	apply := flags.Bool("apply", false, "create the dagr run and dispatch admitted stages")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "start: unexpected positional arguments")
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "start: --file is required")
		return 2
	}
	m, manifestBytes, err := manifest.LoadFileSnapshot(*file)
	if err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		return 1
	}
	mission, err := missioncontrol.Compile(m, *file)
	if err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		return 1
	}
	if !*apply {
		preview := struct {
			Apply     bool                    `json:"apply"`
			Mission   *missioncontrol.Preview `json:"mission,omitempty"`
			Decisions []planner.Decision      `json:"decisions"`
		}{Apply: false, Decisions: planner.Plan(m, nil)}
		if mission.Mode == manifest.MissionDeclarationV1Alpha1 {
			preview.Mission = &mission
		}
		if err := writeJSON(stdout, preview); err != nil {
			fmt.Fprintf(stderr, "start: write preview: %v\n", err)
			return 1
		}
		return 0
	}
	if mission.Mode == manifest.MissionDeclarationV1Alpha1 {
		fmt.Fprintln(stderr, "start: typed mission apply is not available in declaration preview mode")
		return 1
	}
	runtimeManifest, manifestPath, intentPath, err := resolveRuntimeManifest(m, *file)
	if err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		return 1
	}
	store, err := state.Open(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		return 1
	}
	service, err := newCommander(store, runtimeManifest)
	if err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		return 1
	}
	startContext, cancelStart := context.WithTimeout(context.Background(), runtimeManifest.Spec.Limits.CommandDuration())
	defer cancelStart()
	run, err := service.Start(startContext, m, commander.StartInput{ManifestPath: manifestPath, ManifestBytes: manifestBytes, IntentPath: intentPath})
	if err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		if run != nil {
			_ = writeJSON(stdout, BuildStatus(run))
		}
		return 1
	}
	if err := writeJSON(stdout, BuildStatus(run)); err != nil {
		fmt.Fprintf(stderr, "start: write status: %v\n", err)
		return 1
	}
	return 0
}

func runReconcile(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Platoon run ID")
	stateRoot := flags.String("state", ".platoon", "Platoon state root")
	apply := flags.Bool("apply", false, "apply reconciliation mutations")
	poll := flags.String("poll", "", "poll interval for explicit bounded polling mode")
	maxCycles := flags.Int("max-cycles", 60, "maximum polling reconciliation cycles")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "reconcile: unexpected positional arguments")
		return 2
	}
	if *runID == "" {
		fmt.Fprintln(stderr, "reconcile: --run is required")
		return 2
	}
	maxCyclesSet := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "max-cycles" {
			maxCyclesSet = true
		}
	})
	if maxCyclesSet && *poll == "" {
		fmt.Fprintln(stderr, "reconcile: --max-cycles requires --poll")
		return 2
	}
	if !*apply {
		if *poll != "" {
			fmt.Fprintln(stderr, "reconcile: --poll requires --apply")
			return 2
		}
		store, err := state.OpenRead(*stateRoot)
		if err != nil {
			fmt.Fprintf(stderr, "reconcile preview: %v\n", err)
			return 1
		}
		run, err := store.LoadRun(*runID)
		if err != nil {
			fmt.Fprintf(stderr, "reconcile preview: %v\n", err)
			return 1
		}
		preview := struct {
			Apply  bool         `json:"apply"`
			Status StatusReport `json:"status"`
		}{Apply: false, Status: BuildStatus(run)}
		if err := writeJSON(stdout, preview); err != nil {
			return 1
		}
		return 0
	}
	interval := time.Duration(0)
	if *poll != "" {
		var err error
		interval, err = time.ParseDuration(*poll)
		if err != nil || interval < 100*time.Millisecond || interval > time.Hour || *maxCycles < 1 || *maxCycles > 10000 {
			fmt.Fprintln(stderr, "reconcile: --poll must be 100ms through 1h and --max-cycles must be 1 through 10000")
			return 2
		}
	}
	store, err := state.Open(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "reconcile: %v\n", err)
		return 1
	}
	current, err := store.LoadRun(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "reconcile: %v\n", err)
		return 1
	}
	service, err := newCommander(store, &current.Manifest)
	if err != nil {
		fmt.Fprintf(stderr, "reconcile: %v\n", err)
		return 1
	}
	cycles := 1
	if interval > 0 {
		cycles = *maxCycles
	}
	for cycle := 0; cycle < cycles; cycle++ {
		cycleContext, cancelCycle := context.WithTimeout(context.Background(), current.Manifest.Spec.Limits.CommandDuration())
		next, reconcileErr := service.Reconcile(cycleContext, *runID)
		cancelCycle()
		if next != nil {
			current = next
		}
		err = reconcileErr
		if err != nil {
			fmt.Fprintf(stderr, "reconcile: %v\n", err)
			if durable, loadErr := store.LoadRun(*runID); loadErr == nil {
				_ = writeJSON(stdout, BuildStatus(durable))
			}
			return 1
		}
		if current.Status == state.RunCompleted || current.Status == state.RunFailed || interval == 0 || cycle == cycles-1 {
			break
		}
		timer := time.NewTimer(interval)
		<-timer.C
	}
	if err := writeJSON(stdout, BuildStatus(current)); err != nil {
		fmt.Fprintf(stderr, "reconcile: write status: %v\n", err)
		return 1
	}
	return 0
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Platoon run ID")
	stateRoot := flags.String("state", ".platoon", "Platoon state root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "status: unexpected positional arguments")
		return 2
	}
	if *runID == "" {
		fmt.Fprintln(stderr, "status: --run is required")
		return 2
	}
	store, err := state.OpenRead(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}
	run, err := store.LoadRun(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, BuildStatus(run)); err != nil {
		return 1
	}
	return 0
}

func runDrain(args []string, drained bool, stdout, stderr io.Writer) int {
	name := "resume"
	if drained {
		name = "drain"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Platoon run ID")
	stateRoot := flags.String("state", ".platoon", "Platoon state root")
	apply := flags.Bool("apply", false, "apply the run admission state change")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s: unexpected positional arguments\n", name)
		return 2
	}
	if *runID == "" {
		fmt.Fprintf(stderr, "%s: --run is required\n", name)
		return 2
	}
	if !*apply {
		fmt.Fprintf(stderr, "%s: --apply is required\n", name)
		return 2
	}
	store, err := state.Open(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	run, err := store.LoadRun(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	service, err := newCommander(store, &run.Manifest)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	drainContext, cancelDrain := context.WithTimeout(context.Background(), run.Manifest.Spec.Limits.CommandDuration())
	defer cancelDrain()
	run, err = service.SetDrained(drainContext, *runID, drained)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if err := writeJSON(stdout, BuildStatus(run)); err != nil {
		return 1
	}
	return 0
}

func newCommander(store *state.Store, m *manifest.Manifest) (*commander.Commander, error) {
	executor := adapter.OSExecutor{}
	timeout := m.Spec.Limits.CommandDuration()
	maxOutput := m.Spec.Limits.MaxOutputBytes
	authority, err := state.OpenUserAuthority()
	if err != nil {
		return nil, err
	}
	return commander.New(store, commander.Dependencies{
		Dagr:       adapter.NewDagrCLI(m.Spec.Adapters.Dagr, executor, timeout, maxOutput),
		Dispatcher: adapter.NewSergeantCLI(m.Spec.Adapters.Sergeant.Dispatch, executor, timeout, maxOutput),
		Fleets:     adapter.NewFleetReader(m.Spec.Adapters.Sergeant.FleetRoot),
		Diff:       adapter.NewGitInspector(executor, timeout, maxOutput),
		Integrator: commander.CommandIntegrator{Executor: executor, Timeout: timeout, MaxOutput: maxOutput},
		Lease:      state.LeaseOptions{TTL: m.Spec.Limits.LeaseDuration()},
		Authority:  authority,
	}), nil
}

func resolveRuntimeManifest(source *manifest.Manifest, sourceFile string) (*manifest.Manifest, string, string, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, "", "", err
	}
	var result manifest.Manifest
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, "", "", err
	}
	manifestPath, err := filepath.Abs(sourceFile)
	if err != nil {
		return nil, "", "", err
	}
	base := filepath.Dir(manifestPath)
	result.Spec.Adapters.Dagr.Database = resolvePath(base, result.Spec.Adapters.Dagr.Database)
	result.Spec.Adapters.Dagr.Executable = resolveExecutable(base, result.Spec.Adapters.Dagr.Executable)
	result.Spec.Adapters.Dagr.InspectExecutable = resolveExecutable(base, result.Spec.Adapters.Dagr.InspectExecutable)
	result.Spec.Adapters.Sergeant.FleetRoot = resolvePath(base, result.Spec.Adapters.Sergeant.FleetRoot)
	result.Spec.Adapters.Sergeant.Dispatch.Executable = resolveExecutable(base, result.Spec.Adapters.Sergeant.Dispatch.Executable)
	result.Spec.Adapters.Sergeant.Watch.Executable = resolveExecutable(base, result.Spec.Adapters.Sergeant.Watch.Executable)
	result.Spec.Adapters.Sergeant.Wake.Executable = resolveExecutable(base, result.Spec.Adapters.Sergeant.Wake.Executable)
	result.Spec.Adapters.Sergeant.Drain.Executable = resolveExecutable(base, result.Spec.Adapters.Sergeant.Drain.Executable)
	for repositoryIndex := range result.Spec.Repositories {
		repository := &result.Spec.Repositories[repositoryIndex]
		repository.Path = resolvePath(base, repository.Path)
		for commandIndex := range repository.Integration {
			repository.Integration[commandIndex].Executable = resolveExecutable(base, repository.Integration[commandIndex].Executable)
		}
	}
	for stageIndex := range result.Spec.Stages {
		for commandIndex := range result.Spec.Stages[stageIndex].Acceptance {
			command := &result.Spec.Stages[stageIndex].Acceptance[commandIndex]
			command.Executable = resolveExecutable(base, command.Executable)
		}
	}
	return &result, manifestPath, resolvePath(base, source.Spec.Intent), nil
}

func resolvePath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(base, filepath.FromSlash(value))
}

func resolveExecutable(base, value string) string {
	if filepath.IsAbs(value) || (!strings.Contains(value, "/") && !strings.Contains(value, `\`)) {
		return value
	}
	return resolvePath(base, value)
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: platoon <validate|plan|start|reconcile|status|drain|resume> [options]")
}
