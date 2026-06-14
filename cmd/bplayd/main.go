// bplayd serves the Boson playground backend. It can execute commands
// directly for local development, or route each stage through bplay-runner.
// The compile/sandbox/run mechanics live in internal/bpipeline; this binary
// owns the HTTP surface, flags, and embedded frontend.
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/knusbaum/gbasm/internal/bdoc"
	"github.com/knusbaum/gbasm/internal/bpipeline"
)

//go:embed static/*
var staticFiles embed.FS

const (
	maxSourceBytes = 64 << 10
	maxStdinBytes  = 16 << 10
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
	if err := http.ListenAndServe(*addr, logRequests(mux)); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// logRequests logs one line per HTTP request — client, method, path, status,
// and duration — so it is visible when (and whether) requests reach the
// server. Useful when debugging the reverse proxy in front of it.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %s -> %d (%s)", r.RemoteAddr, r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

// statusRecorder captures the response status code for logRequests.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
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
	cfg  serverConfig
	pipe *bpipeline.Pipeline
}

func newServerState(cfg serverConfig) (*serverState, error) {
	if cfg.Mode == "" {
		cfg.Mode = "local"
	}
	if cfg.Docs {
		cfg.DocsBase = normalizeDocsBase(cfg.DocsBase)
		if cfg.DocsBase == "" {
			return nil, errors.New("docs base must not be /")
		}
	}
	pipe, err := bpipeline.New(bpipeline.Config{
		ToolchainDir:  cfg.ToolchainDir,
		RuntimeBundle: cfg.RuntimeBundle,
		Mode:          cfg.Mode,
		RunnerPath:    cfg.RunnerPath,
		CgroupRoot:    cfg.CgroupRoot,
		WorkRoot:      cfg.WorkRoot,
		Timeout:       cfg.Timeout,
		Commit:        toolchainCommit,
	})
	if err != nil {
		return nil, err
	}
	return &serverState{cfg: cfg, pipe: pipe}, nil
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
	if err := s.pipe.Revalidate(); err != nil {
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
	bundle := s.pipe.Bundle()
	writeJSON(w, http.StatusOK, toolchainResponse{
		Commit:                  toolchainCommit,
		BuildTime:               buildTime,
		RuntimeObjects:          bpipeline.RuntimeObjectNames(),
		UserImportablePackages:  bpipeline.UserImportablePackages(),
		RuntimeBundleImportcfg:  bundle.Importcfg,
		RuntimeBundleObjectPath: bundle.ObjectsDir,
	})
}

func (s *serverState) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSourceBytes+maxStdinBytes+4096)
	defer r.Body.Close()

	var req bpipeline.Request
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

	resp := s.pipe.Run(r.Context(), req)
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]bpipeline.ResponseError{
		"error": {Code: errCode, Message: message},
	})
}

type toolchainResponse struct {
	Commit                  string   `json:"commit"`
	BuildTime               string   `json:"buildTime"`
	RuntimeObjects          []string `json:"runtimeObjects"`
	UserImportablePackages  []string `json:"userImportablePackages"`
	RuntimeBundleImportcfg  string   `json:"runtimeBundleImportcfg"`
	RuntimeBundleObjectPath string   `json:"runtimeBundleObjectPath"`
}
