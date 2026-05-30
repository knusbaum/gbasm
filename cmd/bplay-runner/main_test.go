package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	oldWorkdir, oldStdin, oldTimeout, oldMaxOutput := *workdir, *stdinPath, *timeout, *maxOutput
	defer func() {
		*workdir, *stdinPath, *timeout, *maxOutput = oldWorkdir, oldStdin, oldTimeout, oldMaxOutput
	}()
	*workdir = t.TempDir()
	*stdinPath = ""
	*timeout = 2 * time.Second
	*maxOutput = 1024

	res := run([]string{"sh", "-c", "printf hello; printf err >&2; exit 7"})
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if res.Stdout != "hello" {
		t.Fatalf("Stdout = %q", res.Stdout)
	}
	if res.Stderr != "err" {
		t.Fatalf("Stderr = %q", res.Stderr)
	}
}

func TestChildSysProcAttrSandbox(t *testing.T) {
	plain := childSysProcAttr(false)
	if plain.Cloneflags != 0 {
		t.Fatalf("plain Cloneflags = %#x", plain.Cloneflags)
	}
	if !plain.Setpgid {
		t.Fatal("plain Setpgid = false")
	}

	sandboxed := childSysProcAttr(true)
	for _, flag := range []uintptr{
		syscall.CLONE_NEWUSER,
		syscall.CLONE_NEWPID,
		syscall.CLONE_NEWNET,
		syscall.CLONE_NEWIPC,
		syscall.CLONE_NEWUTS,
	} {
		if sandboxed.Cloneflags&flag == 0 {
			t.Fatalf("sandbox Cloneflags missing %#x in %#x", flag, sandboxed.Cloneflags)
		}
	}
	if len(sandboxed.UidMappings) != 1 || sandboxed.UidMappings[0].HostID != os.Getuid() {
		t.Fatalf("UidMappings = %#v", sandboxed.UidMappings)
	}
	if len(sandboxed.GidMappings) != 1 || sandboxed.GidMappings[0].HostID != os.Getgid() {
		t.Fatalf("GidMappings = %#v", sandboxed.GidMappings)
	}
	if sandboxed.GidMappingsEnableSetgroups {
		t.Fatal("GidMappingsEnableSetgroups = true")
	}
}

func TestRunFeedsStdin(t *testing.T) {
	oldWorkdir, oldStdin, oldTimeout, oldMaxOutput := *workdir, *stdinPath, *timeout, *maxOutput
	defer func() {
		*workdir, *stdinPath, *timeout, *maxOutput = oldWorkdir, oldStdin, oldTimeout, oldMaxOutput
	}()
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin")
	if err := os.WriteFile(stdin, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	*workdir = dir
	*stdinPath = stdin
	*timeout = 2 * time.Second
	*maxOutput = 1024

	res := run([]string{"cat"})
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "abc" {
		t.Fatalf("Stdout = %q", res.Stdout)
	}
}

func TestRunTimeoutKillsCommand(t *testing.T) {
	oldWorkdir, oldStdin, oldTimeout, oldMaxOutput := *workdir, *stdinPath, *timeout, *maxOutput
	defer func() {
		*workdir, *stdinPath, *timeout, *maxOutput = oldWorkdir, oldStdin, oldTimeout, oldMaxOutput
	}()
	*workdir = t.TempDir()
	*stdinPath = ""
	*timeout = 25 * time.Millisecond
	*maxOutput = 1024

	res := run([]string{"sh", "-c", "sleep 5"})
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, result = %+v", res)
	}
	if !res.Killed || res.Reason != "timeout" {
		t.Fatalf("Killed/Reason = %v/%q", res.Killed, res.Reason)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	oldWorkdir, oldStdin, oldTimeout, oldMaxOutput := *workdir, *stdinPath, *timeout, *maxOutput
	defer func() {
		*workdir, *stdinPath, *timeout, *maxOutput = oldWorkdir, oldStdin, oldTimeout, oldMaxOutput
	}()
	*workdir = t.TempDir()
	*stdinPath = ""
	*timeout = 2 * time.Second
	*maxOutput = 5

	res := run([]string{"sh", "-c", "printf 123456789"})
	if !res.Truncated {
		t.Fatalf("Truncated = false, result = %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != "12345" {
		t.Fatalf("Stdout = %q", res.Stdout)
	}
}

func TestParseByteLimit(t *testing.T) {
	tests := map[string]uint64{
		"1":     1,
		"64KiB": 64 << 10,
		"64MiB": 64 << 20,
		"2GiB":  2 << 30,
		"5M":    5_000_000,
	}
	for input, want := range tests {
		got, err := parseByteLimit(input)
		if err != nil {
			t.Fatalf("parseByteLimit(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parseByteLimit(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestSetupCgroupWritesConfiguredLimits(t *testing.T) {
	oldRoot, oldMem, oldPids := *cgroupRoot, *memLimit, *pidsMax
	defer func() {
		*cgroupRoot, *memLimit, *pidsMax = oldRoot, oldMem, oldPids
	}()
	root := t.TempDir()
	*cgroupRoot = root
	*memLimit = "64MiB"
	*pidsMax = 8

	cg, err := setupCgroup()
	if err != nil {
		t.Fatal(err)
	}
	defer cg.cleanup()

	if got := readString(t, filepath.Join(cg.path, "memory.max")); got != "67108864" {
		t.Fatalf("memory.max = %q", got)
	}
	if got := readString(t, filepath.Join(cg.path, "pids.max")); got != "8" {
		t.Fatalf("pids.max = %q", got)
	}
}

func TestCgroupKillReason(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte("low 0\nhigh 0\nmax 1\noom 1\noom_kill 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cg := &commandCgroup{path: dir}
	if got := cg.killReason(); got != "oom" {
		t.Fatalf("killReason = %q", got)
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
