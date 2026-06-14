// Package bpipeline is the shared compile/sandbox/run pipeline behind the
// Boson playground and tour. A caller supplies a Config (toolchain location,
// runtime bundle, execution mode, limits), constructs a Pipeline with New,
// and runs untrusted source through it with Run.
//
// This package owns the run mechanics only: command planning, stage
// execution (directly or via bplay-runner), the runtime-bundle contract, and
// the Response wire shape. HTTP handling, flag parsing, and embedded frontend
// assets belong to the cmd binaries (bplayd, btourd) that link this package.
package bpipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// maxOutputBytes caps captured stdout/stderr per stage.
	maxOutputBytes = 64 << 10
)

var (
	compileLimits = runnerLimits{
		CPU:       3 * time.Second,
		Memory:    "256MiB",
		FileSize:  "16MiB",
		OpenFiles: 64,
		Pids:      16,
	}
	programLimits = runnerLimits{
		CPU:       2 * time.Second,
		Memory:    "64MiB",
		FileSize:  "1MiB",
		OpenFiles: 32,
		Pids:      8,
	}
)

// Config is everything the pipeline needs to run sources. It mirrors the
// run-relevant subset of a server's configuration; HTTP/docs concerns stay in
// the cmd binaries.
type Config struct {
	ToolchainDir  string        // directory containing bosc, bas, bld
	RuntimeBundle string        // path to the runtime bundle (importcfg + objects)
	Mode          string        // "local", "runner", or "sandbox"
	RunnerPath    string        // path to bplay-runner when Mode is runner/sandbox
	CgroupRoot    string        // optional delegated cgroup v2 root
	WorkRoot      string        // directory for per-run workspaces (default: system temp)
	Timeout       time.Duration // per-run wall-clock timeout
	Commit        string        // toolchain commit string surfaced in Response.Toolchain
}

// Pipeline runs sources under a validated Config. Construct it with New.
type Pipeline struct {
	cfg    Config
	bundle BundleInfo
	commit string
}

// New validates the Config (toolchain dir, mode, runner path, cgroup root, and
// the runtime bundle) and returns a ready Pipeline.
func New(cfg Config) (*Pipeline, error) {
	if cfg.Timeout <= 0 {
		return nil, errors.New("timeout must be positive")
	}
	if cfg.ToolchainDir != "" {
		abs, err := filepath.Abs(cfg.ToolchainDir)
		if err != nil {
			return nil, fmt.Errorf("toolchain dir: %w", err)
		}
		cfg.ToolchainDir = abs
	}
	if cfg.Mode == "" {
		cfg.Mode = "local"
	}
	if cfg.Mode != "local" && cfg.Mode != "runner" && cfg.Mode != "sandbox" {
		return nil, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
	if cfg.Mode == "runner" || cfg.Mode == "sandbox" {
		abs, err := filepath.Abs(cfg.RunnerPath)
		if err != nil {
			return nil, fmt.Errorf("runner path: %w", err)
		}
		cfg.RunnerPath = abs
		if st, err := os.Stat(cfg.RunnerPath); err != nil {
			return nil, fmt.Errorf("runner path: %w", err)
		} else if st.IsDir() {
			return nil, fmt.Errorf("runner path is a directory: %s", cfg.RunnerPath)
		}
		if cfg.CgroupRoot != "" {
			abs, err := filepath.Abs(cfg.CgroupRoot)
			if err != nil {
				return nil, fmt.Errorf("cgroup root: %w", err)
			}
			cfg.CgroupRoot = abs
			if st, err := os.Stat(cfg.CgroupRoot); err != nil {
				return nil, fmt.Errorf("cgroup root: %w", err)
			} else if !st.IsDir() {
				return nil, fmt.Errorf("cgroup root is not a directory: %s", cfg.CgroupRoot)
			}
		}
	}
	absBundle, err := filepath.Abs(cfg.RuntimeBundle)
	if err != nil {
		return nil, fmt.Errorf("runtime bundle: %w", err)
	}
	cfg.RuntimeBundle = absBundle
	bundle, err := ValidateRuntimeBundle(cfg.RuntimeBundle)
	if err != nil {
		return nil, err
	}
	commit := cfg.Commit
	if commit == "" {
		commit = "dev"
	}
	return &Pipeline{cfg: cfg, bundle: bundle, commit: commit}, nil
}

// Bundle returns the validated runtime-bundle info.
func (p *Pipeline) Bundle() BundleInfo { return p.bundle }

// Commit returns the toolchain commit string surfaced in responses.
func (p *Pipeline) Commit() string { return p.commit }

// Revalidate rechecks the runtime bundle on disk. Used for readiness probes.
func (p *Pipeline) Revalidate() error {
	_, err := ValidateRuntimeBundle(p.cfg.RuntimeBundle)
	return err
}

// Request is a single run of submitted source plus stdin.
type Request struct {
	Source string `json:"source"`
	Stdin  string `json:"stdin"`
}

// Run compiles, assembles, links, and executes the request's source under the
// configured limits, returning the full per-stage Response.
func (p *Pipeline) Run(parent context.Context, req Request) Response {
	workdir, err := os.MkdirTemp(p.cfg.WorkRoot, "bplay-*")
	if err != nil {
		return p.internalError("workspace", err)
	}
	defer os.RemoveAll(workdir)

	sourcePath := filepath.Join(workdir, "main.bos")
	if err := os.WriteFile(sourcePath, []byte(req.Source), 0644); err != nil {
		return p.internalError("write_source", err)
	}
	stdinPath := filepath.Join(workdir, "stdin")
	if err := os.WriteFile(stdinPath, []byte(req.Stdin), 0644); err != nil {
		return p.internalError("write_stdin", err)
	}

	ctx, cancel := context.WithTimeout(parent, p.cfg.Timeout)
	defer cancel()

	plan := commandPlan(p.cfg.ToolchainDir, p.bundle, workdir)
	resp := Response{
		Toolchain: p.commit,
		Status:    StatusOK,
		Artifacts: Artifacts{
			Imports: UserImportablePackages(),
		},
	}

	for _, stage := range []plannedCommand{plan.Bosc, plan.Bas, plan.Bld} {
		step := p.runStage(ctx, stage, "")
		resp.Steps = append(resp.Steps, step)
		if ctx.Err() == context.DeadlineExceeded || step.TimedOut {
			resp.Status = StatusTimeout
			return resp
		}
		if step.ExitCode != 0 {
			resp.Status = statusForStage(stage.Name)
			return resp
		}
	}

	if bs, err := os.ReadFile(filepath.Join(workdir, "main.bs")); err == nil {
		resp.Artifacts.Assembly = string(bs)
	}

	progStep := p.runStage(ctx, plan.Run, stdinPath)
	resp.Program = ProgramResult{
		Stdout:    progStep.Stdout,
		Stderr:    progStep.Stderr,
		ExitCode:  progStep.ExitCode,
		Truncated: progStep.Truncated,
		MS:        progStep.MS,
	}
	if ctx.Err() == context.DeadlineExceeded || progStep.TimedOut {
		resp.Status = StatusTimeout
		resp.Program.Killed = true
		return resp
	}
	if progStep.ExitCode != 0 {
		resp.Status = StatusRuntimeError
		return resp
	}

	resp.Status = StatusOK
	return resp
}

func (p *Pipeline) runStage(ctx context.Context, stage plannedCommand, stdinPath string) StepResult {
	switch p.cfg.Mode {
	case "runner", "sandbox":
		return runCommandViaRunner(ctx, p.cfg.RunnerPath, stage, stdinPath, p.cfg.Timeout, p.cfg.CgroupRoot, p.cfg.Mode == "sandbox")
	default:
		var stdin io.Reader
		var stdinFile *os.File
		if stdinPath != "" {
			f, err := os.Open(stdinPath)
			if err != nil {
				return StepResult{
					Name:     stage.Name,
					Argv:     stage.Argv,
					ExitCode: -1,
					Stderr:   err.Error(),
				}
			}
			stdinFile = f
			stdin = f
		}
		if stdinFile != nil {
			defer stdinFile.Close()
		}
		return runCommand(ctx, stage, stdin)
	}
}

func (p *Pipeline) internalError(stage string, err error) Response {
	return internalError(p.commit, stage, err)
}

type runtimeObject struct {
	Package  string
	Filename string
	Internal bool
}

var runtimeObjects = []runtimeObject{
	{Package: "builtin", Filename: "builtin.bo"},
	{Package: "io", Filename: "io.bo"},
	{Package: "fmt", Filename: "fmt.bo"},
	{Package: "_io_sys", Filename: "_io_sys.bo", Internal: true},
	{Package: "_heap", Filename: "_heap.bo", Internal: true},
	{Package: "_init", Filename: "_init.bo", Internal: true},
	{Package: "_iface", Filename: "_iface.bo", Internal: true},
}

// RuntimeObjectNames returns every runtime package linked into a program, in
// link order.
func RuntimeObjectNames() []string {
	out := make([]string, 0, len(runtimeObjects))
	for _, obj := range runtimeObjects {
		out = append(out, obj.Package)
	}
	return out
}

// UserImportablePackages returns the runtime packages user code may import
// (the non-internal subset).
func UserImportablePackages() []string {
	var out []string
	for _, obj := range runtimeObjects {
		if !obj.Internal {
			out = append(out, obj.Package)
		}
	}
	return out
}

// BundleInfo describes a validated runtime bundle on disk.
type BundleInfo struct {
	Root       string
	ObjectsDir string
	Importcfg  string
	Objects    map[string]string
	Imports    map[string]string
}

// ValidateRuntimeBundle checks that the bundle at root has an importcfg, every
// required runtime object, and an importcfg whose package set exactly matches
// the user-importable packages.
func ValidateRuntimeBundle(root string) (BundleInfo, error) {
	info := BundleInfo{
		Root:       root,
		ObjectsDir: filepath.Join(root, "objects"),
		Importcfg:  filepath.Join(root, "importcfg"),
		Objects:    make(map[string]string),
	}
	if st, err := os.Stat(info.Importcfg); err != nil {
		return info, fmt.Errorf("runtime bundle importcfg: %w", err)
	} else if st.IsDir() {
		return info, fmt.Errorf("runtime bundle importcfg is a directory: %s", info.Importcfg)
	}
	for _, obj := range runtimeObjects {
		path := filepath.Join(info.ObjectsDir, obj.Filename)
		if st, err := os.Stat(path); err != nil {
			return info, fmt.Errorf("runtime object %s: %w", obj.Filename, err)
		} else if st.IsDir() {
			return info, fmt.Errorf("runtime object is a directory: %s", path)
		}
		info.Objects[obj.Package] = path
	}

	imports, err := parseImportcfg(info.Importcfg)
	if err != nil {
		return info, err
	}
	info.Imports = imports

	want := UserImportablePackages()
	sort.Strings(want)
	got := make([]string, 0, len(imports))
	for pkg := range imports {
		got = append(got, pkg)
	}
	sort.Strings(got)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		return info, fmt.Errorf("runtime bundle importcfg packages = %v, want %v", got, want)
	}
	for _, pkg := range want {
		if _, err := os.Stat(imports[pkg]); err != nil {
			return info, fmt.Errorf("importcfg package %s path %s: %w", pkg, imports[pkg], err)
		}
	}
	return info, nil
}

func parseImportcfg(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for lineno, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected name=path", path, lineno+1)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			return nil, fmt.Errorf("%s:%d: empty name or path", path, lineno+1)
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("%s:%d: duplicate package %s", path, lineno+1, name)
		}
		out[name] = value
	}
	return out, nil
}

type commandSet struct {
	Bosc plannedCommand
	Bas  plannedCommand
	Bld  plannedCommand
	Run  plannedCommand
}

type plannedCommand struct {
	Name string
	Argv []string
	Dir  string
}

func commandPlan(toolchainDir string, bundle BundleInfo, workdir string) commandSet {
	mainBos := filepath.Join(workdir, "main.bos")
	mainBS := filepath.Join(workdir, "main.bs")
	mainBO := filepath.Join(workdir, "main.bo")
	mainExe := filepath.Join(workdir, "main")

	bldArgs := []string{toolPath(toolchainDir, "bld"), "-o", mainExe, mainBO}
	for _, obj := range runtimeObjects {
		bldArgs = append(bldArgs, bundle.Objects[obj.Package])
	}

	return commandSet{
		Bosc: plannedCommand{
			Name: "bosc",
			Argv: []string{toolPath(toolchainDir, "bosc"), "-importcfg=" + bundle.Importcfg, "-o", mainBS, mainBos},
			Dir:  workdir,
		},
		Bas: plannedCommand{
			Name: "bas",
			Argv: []string{toolPath(toolchainDir, "bas"), "-o", mainBO, mainBS},
			Dir:  workdir,
		},
		Bld: plannedCommand{
			Name: "bld",
			Argv: bldArgs,
			Dir:  workdir,
		},
		Run: plannedCommand{
			Name: "run",
			Argv: []string{mainExe},
			Dir:  workdir,
		},
	}
}

func toolPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

func runCommand(ctx context.Context, pc plannedCommand, stdin io.Reader) StepResult {
	start := time.Now()
	step := StepResult{Name: pc.Name, Argv: pc.Argv}
	if len(pc.Argv) == 0 {
		step.ExitCode = -1
		step.Stderr = "empty argv"
		return step
	}
	cmd := exec.CommandContext(ctx, pc.Argv[0], pc.Argv[1:]...)
	cmd.Dir = pc.Dir
	cmd.Stdin = stdin
	var stdout, stderr limitBuffer
	stdout.Limit = maxOutputBytes
	stderr.Limit = maxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	step.MS = time.Since(start).Milliseconds()
	step.Stdout = stdout.String()
	step.Stderr = stderr.String()
	step.Truncated = stdout.Truncated || stderr.Truncated
	if err == nil {
		step.ExitCode = 0
		return step
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		step.ExitCode = exitErr.ExitCode()
		return step
	}
	if ctx.Err() == context.DeadlineExceeded {
		step.ExitCode = -1
		step.Stderr = strings.TrimSpace(step.Stderr + "\ntimeout")
		return step
	}
	step.ExitCode = -1
	step.Stderr = strings.TrimSpace(step.Stderr + "\n" + err.Error())
	return step
}

func runCommandViaRunner(ctx context.Context, runner string, pc plannedCommand, stdinPath string, timeout time.Duration, cgroupRoot string, sandbox bool) StepResult {
	step := StepResult{Name: pc.Name, Argv: pc.Argv}
	args := runnerArgs(pc, stdinPath, timeout, cgroupRoot, sandbox)

	cmd := exec.CommandContext(ctx, runner, args...)
	cmd.Dir = pc.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		step.ExitCode = -1
		step.Stderr = strings.TrimSpace(stderr.String() + "\n" + err.Error())
		if ctx.Err() == context.DeadlineExceeded {
			step.TimedOut = true
			step.Stderr = strings.TrimSpace(step.Stderr + "\ntimeout")
		}
		return step
	}

	var result runnerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		step.ExitCode = -1
		step.Stderr = strings.TrimSpace(stderr.String() + "\nrunner returned invalid JSON: " + err.Error())
		return step
	}
	step.ExitCode = result.ExitCode
	step.Stdout = result.Stdout
	step.Stderr = result.Stderr
	step.MS = result.MS
	step.Truncated = result.Truncated
	if result.TimedOut {
		step.TimedOut = true
		step.ExitCode = -1
		step.Stderr = strings.TrimSpace(step.Stderr + "\ntimeout")
	}
	if result.Killed && result.Reason != "" {
		step.Stderr = strings.TrimSpace(step.Stderr + "\nkilled: " + result.Reason)
	}
	return step
}

type runnerLimits struct {
	CPU       time.Duration
	Memory    string
	FileSize  string
	OpenFiles uint64
	Pids      uint64
}

func runnerArgs(pc plannedCommand, stdinPath string, timeout time.Duration, cgroupRoot string, sandbox bool) []string {
	limits := compileLimits
	if pc.Name == "run" {
		limits = programLimits
	}
	args := []string{
		"-workdir", pc.Dir,
		"-timeout", timeout.String(),
		"-max-output", fmt.Sprint(maxOutputBytes),
	}
	if limits.CPU > 0 {
		args = append(args, "-cpu", limits.CPU.String())
	}
	if sandbox {
		args = append(args, "-sandbox")
		if pc.Name == "run" {
			args = append(args, "-static-exec")
		}
	}
	if cgroupRoot != "" {
		args = append(args, "-cgroup-root", cgroupRoot)
	}
	if limits.Memory != "" && cgroupRoot != "" {
		args = append(args, "-mem", limits.Memory)
	}
	if limits.FileSize != "" {
		args = append(args, "-fsize", limits.FileSize)
	}
	if limits.OpenFiles > 0 {
		args = append(args, "-nofile", fmt.Sprint(limits.OpenFiles))
	}
	if limits.Pids > 0 && cgroupRoot != "" {
		args = append(args, "-pids", fmt.Sprint(limits.Pids))
	}
	if stdinPath != "" {
		args = append(args, "-stdin", stdinPath)
	}
	args = append(args, "--")
	args = append(args, pc.Argv...)
	return args
}

type limitBuffer struct {
	Limit     int
	Truncated bool
	buf       bytes.Buffer
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	limit := b.Limit
	if limit <= 0 {
		limit = maxOutputBytes
	}
	remaining := limit - b.buf.Len()
	if remaining <= 0 {
		b.Truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.Truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitBuffer) String() string {
	return b.buf.String()
}

func statusForStage(stage string) Status {
	switch stage {
	case "bosc":
		return StatusCompileError
	case "bas":
		return StatusAssembleError
	case "bld":
		return StatusLinkError
	default:
		return StatusInternalError
	}
}

func internalError(commit, stage string, err error) Response {
	return Response{
		Toolchain: commit,
		Status:    StatusInternalError,
		Error:     &ResponseError{Code: stage, Message: err.Error()},
	}
}

// runnerResult is the JSON protocol emitted by bplay-runner. Keep it small: it
// is mapped into the public Response step shape.
type runnerResult struct {
	ExitCode  int    `json:"exitCode"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	MS        int64  `json:"ms"`
	TimedOut  bool   `json:"timedOut"`
	Killed    bool   `json:"killed"`
	Reason    string `json:"reason,omitempty"`
	Truncated bool   `json:"truncated"`
}

// Status is the overall run outcome.
type Status string

const (
	StatusOK            Status = "ok"
	StatusCompileError  Status = "compile_error"
	StatusAssembleError Status = "assemble_error"
	StatusLinkError     Status = "link_error"
	StatusRuntimeError  Status = "runtime_error"
	StatusTimeout       Status = "timeout"
	StatusInternalError Status = "internal_error"
)

// Response is the full result of a run. Its JSON shape is the public wire
// format shared by every frontend that links this package.
type Response struct {
	Toolchain string         `json:"toolchain"`
	Status    Status         `json:"status"`
	Steps     []StepResult   `json:"steps,omitempty"`
	Artifacts Artifacts      `json:"artifacts,omitempty"`
	Program   ProgramResult  `json:"program,omitempty"`
	Error     *ResponseError `json:"error,omitempty"`
}

type StepResult struct {
	Name      string   `json:"name"`
	Argv      []string `json:"argv"`
	ExitCode  int      `json:"exitCode"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	MS        int64    `json:"ms"`
	Truncated bool     `json:"truncated,omitempty"`
	TimedOut  bool     `json:"-"`
}

type Artifacts struct {
	Assembly string   `json:"assembly,omitempty"`
	Imports  []string `json:"imports,omitempty"`
}

type ProgramResult struct {
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exitCode"`
	Truncated bool   `json:"truncated"`
	Killed    bool   `json:"killed"`
	MS        int64  `json:"ms"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
