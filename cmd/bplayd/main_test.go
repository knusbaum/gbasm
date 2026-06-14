package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/knusbaum/gbasm/internal/bpipeline"
)

func TestPlaygroundRoutes(t *testing.T) {
	state := &serverState{}

	for _, path := range []string{"/", "/play"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		state.handlePlayground(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("Boson Playground")) {
			t.Fatalf("%s did not serve playground HTML", path)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	state.handlePlayground(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/missing status = %d, want 404", rec.Code)
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

func TestRunEndpointHelloIfToolchainBuilt(t *testing.T) {
	state := newBuiltToolchainStateOrSkip(t)

	body := bytes.NewBufferString(`{"source":"package main\n\nimport \"fmt\"\n\nfn main() {\n\tfmt.print(\"hello\\n\")\n}\n"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()
	state.handleRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp bpipeline.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != bpipeline.StatusOK {
		t.Fatalf("run status = %s, response = %+v", resp.Status, resp)
	}
	if resp.Program.Stdout != "hello\n" {
		t.Fatalf("stdout = %q", resp.Program.Stdout)
	}
	if resp.Artifacts.Assembly == "" {
		t.Fatal("assembly artifact is empty")
	}
}

func TestRunEndpointRejectsDirectIOSysImportIfToolchainBuilt(t *testing.T) {
	state := newBuiltToolchainStateOrSkip(t)

	body := bytes.NewBufferString(`{"source":"package main\n\nimport \"_io_sys\"\n\nfn main() {\n}\n"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()
	state.handleRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp bpipeline.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != bpipeline.StatusCompileError {
		t.Fatalf("run status = %s, want %s; response = %+v", resp.Status, bpipeline.StatusCompileError, resp)
	}
}

func TestRunEndpointRunnerModeHelloIfBuilt(t *testing.T) {
	state := newBuiltRunnerStateOrSkip(t)

	body := bytes.NewBufferString(`{"source":"package main\n\nimport \"fmt\"\n\nfn main() {\n\tfmt.print(\"hello runner\\n\")\n}\n"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()
	state.handleRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp bpipeline.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != bpipeline.StatusOK {
		t.Fatalf("run status = %s, response = %+v", resp.Status, resp)
	}
	if resp.Program.Stdout != "hello runner\n" {
		t.Fatalf("stdout = %q", resp.Program.Stdout)
	}
}

func newBuiltToolchainStateOrSkip(t *testing.T) *serverState {
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
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func newBuiltRunnerStateOrSkip(t *testing.T) *serverState {
	t.Helper()
	toolchain := filepath.Clean(filepath.Join("..", ".."))
	for _, name := range []string{"bosc", "bas", "bld", "bplay-runner"} {
		if _, err := os.Stat(filepath.Join(toolchain, name)); err != nil {
			t.Skipf("binary %s not built", name)
		}
	}
	help, err := exec.Command(filepath.Join(toolchain, "bplay-runner"), "-h").CombinedOutput()
	if err != nil && len(help) == 0 {
		t.Skipf("bplay-runner -h failed: %v", err)
	}
	if !strings.Contains(string(help), "-cpu") {
		t.Skip("bplay-runner binary is stale; rebuild with mmk playground")
	}
	bundleRoot := filepath.Join(toolchain, "target", "playground")
	if _, err := bpipeline.ValidateRuntimeBundle(bundleRoot); err != nil {
		t.Skipf("playground runtime bundle not built: %v", err)
	}

	state, err := newServerState(serverConfig{
		ToolchainDir:  toolchain,
		RuntimeBundle: bundleRoot,
		Mode:          "runner",
		RunnerPath:    filepath.Join(toolchain, "bplay-runner"),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
