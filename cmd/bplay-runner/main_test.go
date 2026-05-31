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
		syscall.CLONE_NEWNS,
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

func TestSandboxChildArgvWrapsCommand(t *testing.T) {
	oldWorkdir, oldCPU, oldMem, oldFile, oldOpen, oldCgroup, oldStatic := *workdir, *cpuLimit, *memLimit, *fileLimit, *openFiles, *cgroupRoot, *staticExec
	defer func() {
		*workdir = oldWorkdir
		*cpuLimit = oldCPU
		*memLimit = oldMem
		*fileLimit = oldFile
		*openFiles = oldOpen
		*cgroupRoot = oldCgroup
		*staticExec = oldStatic
	}()
	*workdir = "/tmp/work"
	*cpuLimit = 2 * time.Second
	*memLimit = "64MiB"
	*fileLimit = "1MiB"
	*openFiles = 32
	*cgroupRoot = ""
	*staticExec = true

	got, err := sandboxChildArgv([]string{"/bin/echo", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	wantFlags := []string{
		"-sandbox-child",
		"-workdir", "/tmp/work",
		"-cpu", "2s",
		"-mem", "64MiB",
		"-fsize", "1MiB",
		"-nofile", "32",
		"-static-exec",
		"--",
	}
	if len(got) < 1+len(wantFlags)+2 {
		t.Fatalf("sandbox child argv too short: %#v", got)
	}
	for i, want := range wantFlags {
		if got[i+1] != want {
			t.Fatalf("got[%d] = %q, want %q in %#v", i+1, got[i+1], want, got)
		}
	}
	if got[len(got)-2] != "/bin/echo" || got[len(got)-1] != "hello" {
		t.Fatalf("unexpected wrapped command: %#v", got)
	}
}

func TestSandboxChildArgvOmitsMemoryLimitWhenCgrouped(t *testing.T) {
	oldWorkdir, oldMem, oldCgroup := *workdir, *memLimit, *cgroupRoot
	defer func() {
		*workdir = oldWorkdir
		*memLimit = oldMem
		*cgroupRoot = oldCgroup
	}()
	*workdir = "/tmp/work"
	*memLimit = "64MiB"
	*cgroupRoot = "/sys/fs/cgroup/bplayd"

	got, err := sandboxChildArgv([]string{"/bin/echo"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got, "\x00"), "-mem") {
		t.Fatalf("sandbox child argv includes -mem despite cgroup root: %#v", got)
	}
}

func TestArgvReadPathsSkipsOutputsAndWorkdir(t *testing.T) {
	work := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	importcfg := filepath.Join(bundle, "importcfg")
	builtin := filepath.Join(bundle, "builtin.bo")
	obj := filepath.Join(bundle, "io.bo")
	if err := os.WriteFile(importcfg, []byte("builtin="+builtin+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	argv := []string{
		"/tools/bld",
		"-o", filepath.Join(work, "main"),
		filepath.Join(work, "main.bo"),
		obj,
		"-importcfg=" + importcfg,
		"-o=" + filepath.Join(work, "ignored"),
	}

	got := argvReadPaths(argv, work)
	want := []string{obj, importcfg, builtin}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argvReadPaths = %#v, want %#v", got, want)
	}
}

func TestCollectLandlockPathsIncludesWorkdirAndCommandInputs(t *testing.T) {
	oldStatic := *staticExec
	defer func() {
		*staticExec = oldStatic
	}()
	*staticExec = false

	root := t.TempDir()
	work := filepath.Join(root, "work")
	tools := filepath.Join(root, "tools")
	bundle := filepath.Join(root, "bundle")
	for _, dir := range []string{work, tools, bundle} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	tool := filepath.Join(tools, "bld")
	obj := filepath.Join(bundle, "io.bo")
	for _, path := range []string{tool, obj} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	rules := collectLandlockPaths([]string{tool, "-o", filepath.Join(work, "main"), obj}, work, 3)
	byPath := map[string]uint64{}
	for _, rule := range rules {
		byPath[rule.path] = rule.access
	}
	if byPath[work]&landlockAccessFSWriteFile == 0 {
		t.Fatalf("workdir rule missing write access: %#v", rules)
	}
	if byPath[tool]&landlockAccessFSExecute == 0 {
		t.Fatalf("tool rule missing execute access: %#v", rules)
	}
	if byPath[obj]&landlockAccessFSReadFile == 0 {
		t.Fatalf("object rule missing read access: %#v", rules)
	}
}

func TestCollectLandlockPathsForStaticExecOmitsSystemLibraries(t *testing.T) {
	oldStatic := *staticExec
	defer func() {
		*staticExec = oldStatic
	}()
	*staticExec = true

	work := t.TempDir()
	exe := filepath.Join(work, "main")
	if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}

	rules := collectLandlockPaths([]string{exe}, work, 3)
	for _, rule := range rules {
		switch rule.path {
		case "/lib", "/lib64", "/usr/lib", "/usr/lib64":
			t.Fatalf("static executable rule includes system library path: %#v", rules)
		}
	}
	byPath := map[string]uint64{}
	for _, rule := range rules {
		byPath[rule.path] = rule.access
	}
	if byPath[work]&landlockAccessFSWriteFile == 0 {
		t.Fatalf("workdir rule missing write access: %#v", rules)
	}
	if byPath[exe]&landlockAccessFSExecute == 0 {
		t.Fatalf("executable rule missing execute access: %#v", rules)
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
