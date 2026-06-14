package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/knusbaum/gbasm/internal/bpipeline"
	"github.com/knusbaum/gbasm/internal/tour"
	tourcontent "github.com/knusbaum/gbasm/tour"
)

func contentState(t *testing.T) *serverState {
	t.Helper()
	cat, err := tour.Load(tourcontent.FS())
	if err != nil {
		t.Fatalf("load lessons: %v", err)
	}
	return &serverState{catalog: cat}
}

func TestIndexEndpoint(t *testing.T) {
	state := contentState(t)
	req := httptest.NewRequest(http.MethodGet, "/api/tour", nil)
	rec := httptest.NewRecorder()
	state.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var idx indexResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Sections) == 0 || len(idx.Sections[0].Lessons) == 0 {
		t.Fatalf("empty index: %+v", idx)
	}
	if idx.Sections[0].Lessons[0].Title == "" {
		t.Fatal("first lesson has no title")
	}
}

func TestLessonEndpoint(t *testing.T) {
	state := contentState(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tour/{section}/{lesson}", state.handleLesson)

	req := httptest.NewRequest(http.MethodGet, "/api/tour/01-basics/01-hello", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var lesson lessonResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lesson); err != nil {
		t.Fatal(err)
	}
	if lesson.Title == "" || lesson.Source == "" {
		t.Fatalf("incomplete lesson payload: %+v", lesson)
	}

	miss := httptest.NewRequest(http.MethodGet, "/api/tour/01-basics/nope", nil)
	missRec := httptest.NewRecorder()
	mux.ServeHTTP(missRec, miss)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("missing lesson status = %d, want 404", missRec.Code)
	}
}

func TestStaticAssetRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	rec := httptest.NewRecorder()
	(&serverState{}).staticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static status = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("runProgram")) {
		t.Fatal("static app.js did not contain expected script")
	}
}

func TestRunEndpointAppliesCheckIfToolchainBuilt(t *testing.T) {
	state := newBuiltStateOrSkip(t)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tour/{section}/{lesson}/run", state.handleRun)

	// The hello lesson run as-is should pass its stdout check.
	cat, _ := tour.Load(tourcontent.FS())
	hello, _ := cat.Lookup("01-basics", "01-hello")
	body, _ := json.Marshal(bpipeline.Request{Source: hello.Source})
	req := httptest.NewRequest(http.MethodPost, "/api/tour/01-basics/01-hello/run", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp runResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Run.Status != bpipeline.StatusOK {
		t.Fatalf("run status = %s", resp.Run.Status)
	}
	if !resp.Check.Passed {
		t.Fatalf("check did not pass: %s", resp.Check.Message)
	}

	// The diagnostic lesson run as-is should fail to compile but pass its check.
	move, _ := cat.Lookup("04-ownership", "06-use-after-move")
	body2, _ := json.Marshal(bpipeline.Request{Source: move.Source})
	req2 := httptest.NewRequest(http.MethodPost, "/api/tour/04-ownership/06-use-after-move/run", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	var resp2 runResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Run.Status != bpipeline.StatusCompileError {
		t.Fatalf("diagnostic run status = %s, want compile_error", resp2.Run.Status)
	}
	if !resp2.Check.Passed {
		t.Fatalf("diagnostic check did not pass: %s", resp2.Check.Message)
	}
}

func newBuiltStateOrSkip(t *testing.T) *serverState {
	t.Helper()
	toolchain := filepath.Clean(filepath.Join("..", ".."))
	for _, name := range []string{"bosc", "bas", "bld"} {
		if _, err := os.Stat(filepath.Join(toolchain, name)); err != nil {
			t.Skipf("toolchain binary %s not built", name)
		}
	}
	bundleRoot := filepath.Join(toolchain, "target", "playground")
	if _, err := bpipeline.ValidateRuntimeBundle(bundleRoot); err != nil {
		t.Skipf("playground runtime bundle not built: %v", err)
	}
	state, err := newServerState(serverConfig{
		ToolchainDir:  toolchain,
		RuntimeBundle: bundleRoot,
		Timeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
