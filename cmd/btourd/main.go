// btourd serves the Boson tour: a guided sequence of runnable lessons. It
// links the shared internal/bpipeline run pipeline (the same one behind the
// playground) and serves its own lesson content and frontend. It is a separate
// service from bplayd — separate binary, endpoints, and UI.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"html"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/knusbaum/gbasm/internal/bpipeline"
	"github.com/knusbaum/gbasm/internal/tour"
	tourcontent "github.com/knusbaum/gbasm/tour"
	"github.com/yuin/goldmark"
)

// markdown renders lesson prose. Lesson content is trusted (authored in this
// repo), so the default CommonMark renderer is used without raw-HTML pass-
// through. Configured once and reused; goldmark.Markdown is concurrency-safe.
var markdown = goldmark.New()

// renderMarkdown converts a lesson's Markdown prose to HTML. On the unlikely
// error it falls back to the raw text wrapped in a <pre> so the lesson still
// shows something rather than blanking.
func renderMarkdown(src string) string {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return "<pre>" + html.EscapeString(src) + "</pre>"
	}
	return buf.String()
}

//go:embed static/*
var staticFiles embed.FS

const (
	maxSourceBytes = 64 << 10
	maxStdinBytes  = 16 << 10
)

var (
	addr          = flag.String("addr", ":8087", "HTTP listen address")
	mode          = flag.String("mode", "local", "Execution mode: local, runner, or sandbox")
	toolchainDir  = flag.String("toolchain-dir", ".", "Directory containing bosc, bas, and bld")
	runtimeBundle = flag.String("runtime-bundle", "target/playground", "Directory containing the runtime importcfg and objects")
	runnerPath    = flag.String("runner", "./bplay-runner", "Path to bplay-runner when -mode=runner")
	cgroupRoot    = flag.String("cgroup-root", "", "Optional delegated cgroup v2 root for bplay-runner")
	workRoot      = flag.String("work-root", "", "Directory for per-run workspaces (default: system temp)")
	runTimeout    = flag.Duration("timeout", 5*time.Second, "Per-run wall-clock timeout")
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	if *mode != "local" && *mode != "runner" && *mode != "sandbox" {
		log.Fatalf("unsupported -mode %q: supported modes are local, runner, and sandbox", *mode)
	}

	state, err := newServerState(serverConfig{
		ToolchainDir:  *toolchainDir,
		RuntimeBundle: *runtimeBundle,
		Mode:          *mode,
		RunnerPath:    *runnerPath,
		CgroupRoot:    *cgroupRoot,
		WorkRoot:      *workRoot,
		Timeout:       *runTimeout,
	})
	if err != nil {
		log.Fatalf("btourd: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tour", state.handleIndex)
	mux.HandleFunc("GET /api/tour/{section}/{lesson}", state.handleLesson)
	mux.HandleFunc("POST /api/tour/{section}/{lesson}/run", state.handleRun)
	mux.HandleFunc("/healthz", state.handleHealthz)
	mux.HandleFunc("/readyz", state.handleReadyz)
	mux.HandleFunc("/", state.handleTour)
	mux.Handle("/static/", state.staticHandler())

	log.Printf("btourd: listening on %s", *addr)
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
}

type serverState struct {
	pipe    *bpipeline.Pipeline
	catalog *tour.Catalog
}

func newServerState(cfg serverConfig) (*serverState, error) {
	catalog, err := tour.Load(tourcontent.FS())
	if err != nil {
		return nil, err
	}
	if len(catalog.Sections) == 0 {
		return nil, errors.New("no tour lessons found")
	}
	pipe, err := bpipeline.New(bpipeline.Config{
		ToolchainDir:  cfg.ToolchainDir,
		RuntimeBundle: cfg.RuntimeBundle,
		Mode:          cfg.Mode,
		RunnerPath:    cfg.RunnerPath,
		CgroupRoot:    cfg.CgroupRoot,
		WorkRoot:      cfg.WorkRoot,
		Timeout:       cfg.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &serverState{pipe: pipe, catalog: catalog}, nil
}

func (s *serverState) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
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

func (s *serverState) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

func (s *serverState) handleTour(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" {
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

// indexResponse is the lesson index: ordered sections, each with ordered
// lesson summaries.
type indexResponse struct {
	Sections []indexSection `json:"sections"`
}

type indexSection struct {
	Dir     string        `json:"dir"`
	Name    string        `json:"name"`
	Lessons []indexLesson `json:"lessons"`
}

type indexLesson struct {
	Section string `json:"section"`
	Slug    string `json:"slug"`
	Title   string `json:"title"`
}

func (s *serverState) handleIndex(w http.ResponseWriter, r *http.Request) {
	var idx indexResponse
	for _, sec := range s.catalog.Sections {
		is := indexSection{Dir: sec.Dir, Name: sec.Name}
		for _, l := range sec.Lessons {
			is.Lessons = append(is.Lessons, indexLesson{Section: l.Section, Slug: l.Slug, Title: l.Title})
		}
		idx.Sections = append(idx.Sections, is)
	}
	writeJSON(w, http.StatusOK, idx)
}

// lessonResponse is a single lesson's full payload for the editor view.
type lessonResponse struct {
	Section     string `json:"section"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	SectionName string `json:"sectionName"`
	ProseHTML   string `json:"proseHTML"`
	Source      string `json:"source"`
	Expected    string `json:"expected,omitempty"`
	HasExpected bool   `json:"hasExpected"`
	Diagnostic  bool   `json:"diagnostic"`
}

func (s *serverState) handleLesson(w http.ResponseWriter, r *http.Request) {
	l, ok := s.catalog.Lookup(r.PathValue("section"), r.PathValue("lesson"))
	if !ok {
		writeError(w, http.StatusNotFound, "no_such_lesson", "lesson not found")
		return
	}
	writeJSON(w, http.StatusOK, lessonResponse{
		Section:     l.Section,
		Slug:        l.Slug,
		Title:       l.Title,
		SectionName: l.SectionName,
		ProseHTML:   renderMarkdown(l.Prose),
		Source:      l.Source,
		Expected:    l.Expected,
		HasExpected: l.HasExpected,
		Diagnostic:  l.Check != nil && l.Check.Kind == "diagnostic",
	})
}

// runResponse pairs a pipeline run with the lesson's check outcome. The run
// sub-object is shape-compatible with bplayd's /api/run response.
type runResponse struct {
	Run   bpipeline.Response `json:"run"`
	Check tour.CheckResult   `json:"check"`
}

func (s *serverState) handleRun(w http.ResponseWriter, r *http.Request) {
	l, ok := s.catalog.Lookup(r.PathValue("section"), r.PathValue("lesson"))
	if !ok {
		writeError(w, http.StatusNotFound, "no_such_lesson", "lesson not found")
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
	writeJSON(w, http.StatusOK, runResponse{
		Run:   resp,
		Check: l.Verify(resp),
	})
}

// writeJSON and writeError below mirror bplayd's response helpers.

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
