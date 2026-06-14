package bpipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestValidateRuntimeBundle(t *testing.T) {
	root := writeTestBundle(t, map[string]string{
		"builtin": "builtin.bo",
		"io":      "io.bo",
		"fmt":     "fmt.bo",
	})

	info, err := ValidateRuntimeBundle(root)
	if err != nil {
		t.Fatalf("ValidateRuntimeBundle: %v", err)
	}
	if info.Importcfg != filepath.Join(root, "importcfg") {
		t.Fatalf("Importcfg = %q", info.Importcfg)
	}
	if got := info.Objects["_io_sys"]; got != filepath.Join(root, "objects", "_io_sys.bo") {
		t.Fatalf("_io_sys object = %q", got)
	}
}

func TestValidateRuntimeBundleRejectsInternalImportcfgEntry(t *testing.T) {
	root := writeTestBundle(t, map[string]string{
		"builtin": "builtin.bo",
		"io":      "io.bo",
		"fmt":     "fmt.bo",
		"_io_sys": "_io_sys.bo",
	})

	_, err := ValidateRuntimeBundle(root)
	if err == nil {
		t.Fatal("ValidateRuntimeBundle succeeded with _io_sys in importcfg")
	}
}

func TestValidateRuntimeBundleRejectsMissingObject(t *testing.T) {
	root := writeTestBundle(t, map[string]string{
		"builtin": "builtin.bo",
		"io":      "io.bo",
		"fmt":     "fmt.bo",
	})
	if err := os.Remove(filepath.Join(root, "objects", "io.bo")); err != nil {
		t.Fatal(err)
	}

	_, err := ValidateRuntimeBundle(root)
	if err == nil {
		t.Fatal("ValidateRuntimeBundle succeeded with missing io.bo")
	}
}

func TestCommandPlan(t *testing.T) {
	root := writeTestBundle(t, map[string]string{
		"builtin": "builtin.bo",
		"io":      "io.bo",
		"fmt":     "fmt.bo",
	})
	bundle, err := ValidateRuntimeBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(t.TempDir(), "run")
	plan := commandPlan("/tools", bundle, workdir)

	wantBosc := []string{"/tools/bosc", "-importcfg=" + filepath.Join(root, "importcfg"), "-o", filepath.Join(workdir, "main.bs"), filepath.Join(workdir, "main.bos")}
	if !reflect.DeepEqual(plan.Bosc.Argv, wantBosc) {
		t.Fatalf("bosc argv\n got: %#v\nwant: %#v", plan.Bosc.Argv, wantBosc)
	}

	wantBld := []string{
		"/tools/bld", "-o", filepath.Join(workdir, "main"), filepath.Join(workdir, "main.bo"),
		filepath.Join(root, "objects", "builtin.bo"),
		filepath.Join(root, "objects", "io.bo"),
		filepath.Join(root, "objects", "fmt.bo"),
		filepath.Join(root, "objects", "_io_sys.bo"),
		filepath.Join(root, "objects", "_heap.bo"),
		filepath.Join(root, "objects", "_init.bo"),
		filepath.Join(root, "objects", "_iface.bo"),
	}
	if !reflect.DeepEqual(plan.Bld.Argv, wantBld) {
		t.Fatalf("bld argv\n got: %#v\nwant: %#v", plan.Bld.Argv, wantBld)
	}
}

func TestRunnerArgsUseStageLimits(t *testing.T) {
	pc := plannedCommand{
		Name: "bosc",
		Dir:  "/tmp/work",
		Argv: []string{"/tools/bosc", "main.bos"},
	}
	got := runnerArgs(pc, "", 5*time.Second, "", false)
	want := []string{
		"-workdir", "/tmp/work",
		"-timeout", "5s",
		"-max-output", "65536",
		"-cpu", "3s",
		"-fsize", "16MiB",
		"-nofile", "64",
		"--",
		"/tools/bosc", "main.bos",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runner args\n got: %#v\nwant: %#v", got, want)
	}

	pc.Name = "run"
	got = runnerArgs(pc, "/tmp/work/stdin", 5*time.Second, "", false)
	want = []string{
		"-workdir", "/tmp/work",
		"-timeout", "5s",
		"-max-output", "65536",
		"-cpu", "2s",
		"-fsize", "1MiB",
		"-nofile", "32",
		"-stdin", "/tmp/work/stdin",
		"--",
		"/tools/bosc", "main.bos",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("run runner args\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRunnerArgsUseCgroupLimitsWhenConfigured(t *testing.T) {
	pc := plannedCommand{
		Name: "run",
		Dir:  "/tmp/work",
		Argv: []string{"/tmp/work/main"},
	}
	got := runnerArgs(pc, "", 5*time.Second, "/sys/fs/cgroup/bplayd", false)
	want := []string{
		"-workdir", "/tmp/work",
		"-timeout", "5s",
		"-max-output", "65536",
		"-cpu", "2s",
		"-cgroup-root", "/sys/fs/cgroup/bplayd",
		"-mem", "64MiB",
		"-fsize", "1MiB",
		"-nofile", "32",
		"-pids", "8",
		"--",
		"/tmp/work/main",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cgroup runner args\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRunnerArgsUseSandboxFlag(t *testing.T) {
	pc := plannedCommand{
		Name: "run",
		Dir:  "/tmp/work",
		Argv: []string{"/tmp/work/main"},
	}
	got := runnerArgs(pc, "", 5*time.Second, "", true)
	want := []string{
		"-workdir", "/tmp/work",
		"-timeout", "5s",
		"-max-output", "65536",
		"-cpu", "2s",
		"-sandbox",
		"-static-exec",
		"-fsize", "1MiB",
		"-nofile", "32",
		"--",
		"/tmp/work/main",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox runner args\n got: %#v\nwant: %#v", got, want)
	}
}

func writeTestBundle(t *testing.T, imports map[string]string) string {
	t.Helper()
	root := t.TempDir()
	objectsDir := filepath.Join(root, "objects")
	if err := os.MkdirAll(objectsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, obj := range runtimeObjects {
		if err := os.WriteFile(filepath.Join(objectsDir, obj.Filename), []byte(obj.Package), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var cfg bytes.Buffer
	for _, pkg := range []string{"builtin", "io", "fmt", "_io_sys", "_iface"} {
		name, ok := imports[pkg]
		if !ok {
			continue
		}
		cfg.WriteString(pkg)
		cfg.WriteByte('=')
		cfg.WriteString(filepath.Join(objectsDir, name))
		cfg.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(root, "importcfg"), cfg.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}
