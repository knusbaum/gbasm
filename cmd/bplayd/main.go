// bplayd serves the Boson playground backend. It can execute commands
// directly for local development, or route each stage through bplay-runner.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/knusbaum/gbasm/internal/bdoc"
)

//go:embed static/*
var staticFiles embed.FS

const (
	maxSourceBytes = 64 << 10
	maxStdinBytes  = 16 << 10
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

var (
	addr          = flag.String("addr", ":8086", "HTTP listen address")
	mode          = flag.String("mode", "local", "Execution mode: local, runner, or sandbox")
	toolchainDir  = flag.String("toolchain-dir", ".", "Directory containing bosc, bas, and bld")
	runtimeBundle = flag.String("runtime-bundle", "target/playground", "Directory containing playground importcfg and objects")
	runnerPath    = flag.String("runner", "./bplay-runner", "Path to bplay-runner when -mode=runner")
	cgroupRoot    = flag.String("cgroup-root", "", "Optional delegated cgroup v2 root for bplay-runner")
	workRoot      = flag.String("work-root", "", "Directory for per-run workspaces (default: system temp)")
	runTimeout    = flag.Duration("timeout", 5*time.Second, "Per-run wall-clock timeout")
	docs          = flag.Bool("docs", true, "Serve bdoc documentation")
	docsBase      = flag.String("docs-base", "/docs", "Base URL path for documentation")
	docsBosonPath = flag.String("docs-bosonpath", "runtime", "Colon-separated package search path for documentation")
)

var (
	toolchainCommit = "dev"
	buildTime       = ""
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	if *mode != "local" && *mode != "runner" && *mode != "sandbox" {
		log.Fatalf("unsupported -mode %q: supported modes are local, runner, and sandbox", *mode)
	}

	cfg := serverConfig{
		ToolchainDir:  *toolchainDir,
		RuntimeBundle: *runtimeBundle,
		Mode:          *mode,
		RunnerPath:    *runnerPath,
		CgroupRoot:    *cgroupRoot,
		WorkRoot:      *workRoot,
		Timeout:       *runTimeout,
		Docs:          *docs,
		DocsBase:      *docsBase,
		DocsBosonPath: *docsBosonPath,
	}
	state, err := newServerState(cfg)
	if err != nil {
		log.Fatalf("bplayd: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/run", state.handleRun)
	mux.HandleFunc("/api/toolchain", state.handleToolchain)
	mux.HandleFunc("/healthz", state.handleHealthz)
	mux.HandleFunc("/readyz", state.handleReadyz)
	if state.cfg.Docs {
		docsHandler := bdoc.Handler(bdoc.Options{BosonPath: state.cfg.DocsBosonPath, BasePath: state.cfg.DocsBase})
		mux.Handle(state.cfg.DocsBase, docsHandler)
		mux.Handle(state.cfg.DocsBase+"/", docsHandler)
	}
	mux.HandleFunc("/", state.handlePlayground)
	mux.Handle("/static/", state.staticHandler())

	log.Printf("bplayd: listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

type serverConfig struct {
	ToolchainDir  string
	RuntimeBundle string
	Mode          string
	RunnerPath    string
	CgroupRoot    string
	WorkRoot      string
	Timeout       time.Duration
	Docs          bool
	DocsBase      string
	DocsBosonPath string
}

type serverState struct {
	cfg    serverConfig
	bundle runtimeBundleInfo
}

func newServerState(cfg serverConfig) (*serverState, error) {
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
	if cfg.Docs {
		cfg.DocsBase = normalizeDocsBase(cfg.DocsBase)
		if cfg.DocsBase == "" {
			return nil, errors.New("docs base must not be /")
		}
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
	bundle, err := validateRuntimeBundle(cfg.RuntimeBundle)
	if err != nil {
		return nil, err
	}
	return &serverState{cfg: cfg, bundle: bundle}, nil
}

func normalizeDocsBase(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "/docs"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	return base
}

func (s *serverState) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *serverState) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

func (s *serverState) handlePlayground(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/play" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	b, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(b)
}

func (s *serverState) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := validateRuntimeBundle(s.cfg.RuntimeBundle); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *serverState) handleToolchain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, toolchainResponse{
		Commit:                  toolchainCommit,
		BuildTime:               buildTime,
		RuntimeObjects:          runtimeObjectNames(),
		UserImportablePackages:  userImportablePackages(),
		RuntimeBundleImportcfg:  s.bundle.Importcfg,
		RuntimeBundleObjectPath: s.bundle.ObjectsDir,
	})
}

func (s *serverState) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSourceBytes+maxStdinBytes+4096)
	defer r.Body.Close()

	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if len(req.Source) > maxSourceBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "source_too_large", "source exceeds 64 KiB")
		return
	}
	if len(req.Stdin) > maxStdinBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "stdin_too_large", "stdin exceeds 16 KiB")
		return
	}

	resp := s.run(r.Context(), req)
	writeJSON(w, http.StatusOK, resp)
}

func (s *serverState) run(parent context.Context, req runRequest) runResponse {
	workdir, err := os.MkdirTemp(s.cfg.WorkRoot, "bplay-*")
	if err != nil {
		return internalError("workspace", err)
	}
	defer os.RemoveAll(workdir)

	sourcePath := filepath.Join(workdir, "main.bos")
	if err := os.WriteFile(sourcePath, []byte(req.Source), 0644); err != nil {
		return internalError("write_source", err)
	}
	stdinPath := filepath.Join(workdir, "stdin")
	if err := os.WriteFile(stdinPath, []byte(req.Stdin), 0644); err != nil {
		return internalError("write_stdin", err)
	}

	ctx, cancel := context.WithTimeout(parent, s.cfg.Timeout)
	defer cancel()

	plan := commandPlan(s.cfg.ToolchainDir, s.bundle, workdir)
	resp := runResponse{
		Toolchain: toolchainCommit,
		Status:    statusOK,
		Artifacts: artifacts{
			Imports: userImportablePackages(),
		},
	}

	for _, stage := range []plannedCommand{plan.Bosc, plan.Bas, plan.Bld} {
		step := s.runStage(ctx, stage, "")
		resp.Steps = append(resp.Steps, step)
		if ctx.Err() == context.DeadlineExceeded || step.TimedOut {
			resp.Status = statusTimeout
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

	progStep := s.runStage(ctx, plan.Run, stdinPath)
	resp.Program = programResult{
		Stdout:    progStep.Stdout,
		Stderr:    progStep.Stderr,
		ExitCode:  progStep.ExitCode,
		Truncated: progStep.Truncated,
		MS:        progStep.MS,
	}
	if ctx.Err() == context.DeadlineExceeded || progStep.TimedOut {
		resp.Status = statusTimeout
		resp.Program.Killed = true
		return resp
	}
	if progStep.ExitCode != 0 {
		resp.Status = statusRuntimeError
		return resp
	}

	resp.Status = statusOK
	return resp
}

func (s *serverState) runStage(ctx context.Context, stage plannedCommand, stdinPath string) stepResult {
	switch s.cfg.Mode {
	case "runner", "sandbox":
		return runCommandViaRunner(ctx, s.cfg.RunnerPath, stage, stdinPath, s.cfg.Timeout, s.cfg.CgroupRoot, s.cfg.Mode == "sandbox")
	default:
		var stdin io.Reader
		var stdinFile *os.File
		if stdinPath != "" {
			f, err := os.Open(stdinPath)
			if err != nil {
				return stepResult{
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

type runtimeObject struct {
	Package  string
	Filename string
	Internal bool
}

var runtimeObjects = []runtimeObject{
	{Package: "builtin", Filename: "builtin.bo"},
	{Package: "string", Filename: "string.bo"},
	{Package: "io", Filename: "io.bo"},
	{Package: "fmt", Filename: "fmt.bo"},
	{Package: "_io_sys", Filename: "_io_sys.bo", Internal: true},
	{Package: "_heap", Filename: "_heap.bo", Internal: true},
	{Package: "_init", Filename: "_init.bo", Internal: true},
	{Package: "_iface", Filename: "_iface.bo", Internal: true},
}

func runtimeObjectNames() []string {
	out := make([]string, 0, len(runtimeObjects))
	for _, obj := range runtimeObjects {
		out = append(out, obj.Package)
	}
	return out
}

func userImportablePackages() []string {
	var out []string
	for _, obj := range runtimeObjects {
		if !obj.Internal {
			out = append(out, obj.Package)
		}
	}
	return out
}

type runtimeBundleInfo struct {
	Root       string
	ObjectsDir string
	Importcfg  string
	Objects    map[string]string
	Imports    map[string]string
}

func validateRuntimeBundle(root string) (runtimeBundleInfo, error) {
	info := runtimeBundleInfo{
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

	want := userImportablePackages()
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

func commandPlan(toolchainDir string, bundle runtimeBundleInfo, workdir string) commandSet {
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

func runCommand(ctx context.Context, pc plannedCommand, stdin io.Reader) stepResult {
	start := time.Now()
	step := stepResult{Name: pc.Name, Argv: pc.Argv}
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

func runCommandViaRunner(ctx context.Context, runner string, pc plannedCommand, stdinPath string, timeout time.Duration, cgroupRoot string, sandbox bool) stepResult {
	step := stepResult{Name: pc.Name, Argv: pc.Argv}
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

func statusForStage(stage string) runStatus {
	switch stage {
	case "bosc":
		return statusCompileError
	case "bas":
		return statusAssembleError
	case "bld":
		return statusLinkError
	default:
		return statusInternalError
	}
}

func internalError(stage string, err error) runResponse {
	return runResponse{
		Toolchain: toolchainCommit,
		Status:    statusInternalError,
		Error:     &responseError{Code: stage, Message: err.Error()},
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]responseError{
		"error": {Code: errCode, Message: message},
	})
}

type runStatus string

const (
	statusOK            runStatus = "ok"
	statusCompileError  runStatus = "compile_error"
	statusAssembleError runStatus = "assemble_error"
	statusLinkError     runStatus = "link_error"
	statusRuntimeError  runStatus = "runtime_error"
	statusTimeout       runStatus = "timeout"
	statusInternalError runStatus = "internal_error"
)

type runRequest struct {
	Source string `json:"source"`
	Stdin  string `json:"stdin"`
}

type runResponse struct {
	Toolchain string         `json:"toolchain"`
	Status    runStatus      `json:"status"`
	Steps     []stepResult   `json:"steps,omitempty"`
	Artifacts artifacts      `json:"artifacts,omitempty"`
	Program   programResult  `json:"program,omitempty"`
	Error     *responseError `json:"error,omitempty"`
}

type stepResult struct {
	Name      string   `json:"name"`
	Argv      []string `json:"argv"`
	ExitCode  int      `json:"exitCode"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	MS        int64    `json:"ms"`
	Truncated bool     `json:"truncated,omitempty"`
	TimedOut  bool     `json:"-"`
}

type artifacts struct {
	Assembly string   `json:"assembly,omitempty"`
	Imports  []string `json:"imports,omitempty"`
}

type programResult struct {
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exitCode"`
	Truncated bool   `json:"truncated"`
	Killed    bool   `json:"killed"`
	MS        int64  `json:"ms"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type toolchainResponse struct {
	Commit                  string   `json:"commit"`
	BuildTime               string   `json:"buildTime"`
	RuntimeObjects          []string `json:"runtimeObjects"`
	UserImportablePackages  []string `json:"userImportablePackages"`
	RuntimeBundleImportcfg  string   `json:"runtimeBundleImportcfg"`
	RuntimeBundleObjectPath string   `json:"runtimeBundleObjectPath"`
}
