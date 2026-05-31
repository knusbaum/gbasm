// bplay-runner executes one playground command and reports a JSON result.
//
// This first runner slice is a protocol/process-boundary implementation. It
// applies wall-clock timeout and bounded output capture; later slices add
// namespaces, cgroups, and seccomp behind the same command-line contract.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	workdir    = flag.String("workdir", "", "Working directory for the command")
	stdinPath  = flag.String("stdin", "", "Optional stdin file")
	timeout    = flag.Duration("timeout", 5*time.Second, "Wall-clock timeout")
	maxOutput  = flag.Int("max-output", 64<<10, "Maximum bytes captured per stream")
	cpuLimit   = flag.Duration("cpu", 0, "Optional RLIMIT_CPU limit")
	memLimit   = flag.String("mem", "", "Optional memory limit, e.g. 64MiB. Uses cgroup memory.max when -cgroup-root is set; otherwise RLIMIT_AS.")
	fileLimit  = flag.String("fsize", "", "Optional RLIMIT_FSIZE limit, e.g. 1MiB")
	openFiles  = flag.Uint64("nofile", 0, "Optional RLIMIT_NOFILE soft/hard limit")
	cgroupRoot = flag.String("cgroup-root", "", "Optional delegated cgroup v2 root for per-command cgroups")
	pidsMax    = flag.Uint64("pids", 0, "Optional cgroup pids.max limit")
	sandbox    = flag.Bool("sandbox", false, "Run command in user, pid, network, ipc, and uts namespaces")
	staticExec = flag.Bool("static-exec", false, "Sandboxed command is statically linked; do not allow system library paths")
	child      = flag.Bool("sandbox-child", false, "Internal sandbox child mode")
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	argv := flag.Args()
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if *workdir == "" {
		writeFatal("missing -workdir")
	}
	if len(argv) == 0 {
		writeFatal("missing command after --")
	}
	if *timeout <= 0 {
		writeFatal("timeout must be positive")
	}
	if *maxOutput <= 0 {
		writeFatal("max-output must be positive")
	}
	if *child {
		os.Exit(runSandboxChild(argv))
	}

	result := run(argv)
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		log.Fatalf("write result: %v", err)
	}
}

func run(argv []string) runnerResult {
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var stdin io.Reader
	var stdinFile *os.File
	if *stdinPath != "" {
		f, err := os.Open(*stdinPath)
		if err != nil {
			return runnerResult{ExitCode: -1, Stderr: err.Error(), Killed: true, Reason: "stdin_open"}
		}
		stdinFile = f
		stdin = f
	}
	if stdinFile != nil {
		defer stdinFile.Close()
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if *sandbox {
		childArgv, err := sandboxChildArgv(argv)
		if err != nil {
			return runnerResult{ExitCode: -1, Stderr: err.Error(), MS: time.Since(start).Milliseconds(), Killed: true, Reason: "sandbox"}
		}
		cmd = exec.CommandContext(ctx, childArgv[0], childArgv[1:]...)
	}
	cmd.Dir = *workdir
	cmd.Stdin = stdin
	cmd.SysProcAttr = childSysProcAttr(*sandbox)

	var stdout, stderr limitBuffer
	stdout.Limit = *maxOutput
	stderr.Limit = *maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cg, err := setupCgroup()
	if err != nil {
		return runnerResult{ExitCode: -1, Stderr: err.Error(), MS: time.Since(start).Milliseconds(), Killed: true, Reason: "cgroup"}
	}
	if cg != nil {
		defer cg.cleanup()
	}

	if !*sandbox {
		limits, err := buildRLimits()
		if err != nil {
			return runnerResult{ExitCode: -1, Stderr: err.Error(), MS: time.Since(start).Milliseconds(), Killed: true, Reason: "limits"}
		}
		if err := applyRLimits(limits); err != nil {
			return runnerResult{ExitCode: -1, Stderr: err.Error(), MS: time.Since(start).Milliseconds(), Killed: true, Reason: "limits"}
		}
	}

	err = cmd.Start()
	if err != nil {
		return runnerResult{ExitCode: -1, Stderr: err.Error(), MS: time.Since(start).Milliseconds(), Killed: true, Reason: "start"}
	}
	if cg != nil {
		if err := cg.addProcess(cmd.Process.Pid); err != nil {
			killProcessGroup(cmd.Process.Pid)
			_ = cmd.Wait()
			return runnerResult{ExitCode: -1, Stderr: err.Error(), MS: time.Since(start).Milliseconds(), Killed: true, Reason: "cgroup"}
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var timedOut bool
	select {
	case err = <-done:
	case <-ctx.Done():
		timedOut = true
		if cg != nil {
			_ = cg.kill()
		}
		killProcessGroup(cmd.Process.Pid)
		err = <-done
	}

	res := runnerResult{
		ExitCode:  exitCode(err),
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		MS:        time.Since(start).Milliseconds(),
		TimedOut:  timedOut,
		Killed:    timedOut,
		Truncated: stdout.Truncated || stderr.Truncated,
	}
	if timedOut {
		res.Reason = "timeout"
	}
	if cg != nil {
		if reason := cg.killReason(); reason != "" && res.ExitCode != 0 {
			res.Killed = true
			res.Reason = reason
		}
	}
	if err != nil && res.ExitCode == -1 && !timedOut {
		res.Killed = true
		res.Reason = "exec"
		if res.Stderr == "" {
			res.Stderr = err.Error()
		}
	}
	return res
}

func childSysProcAttr(useSandbox bool) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if !useSandbox {
		return attr
	}
	attr.Cloneflags = syscall.CLONE_NEWUSER |
		syscall.CLONE_NEWPID |
		syscall.CLONE_NEWNET |
		syscall.CLONE_NEWIPC |
		syscall.CLONE_NEWUTS |
		syscall.CLONE_NEWNS
	attr.UidMappings = []syscall.SysProcIDMap{{
		ContainerID: 0,
		HostID:      os.Getuid(),
		Size:        1,
	}}
	attr.GidMappings = []syscall.SysProcIDMap{{
		ContainerID: 0,
		HostID:      os.Getgid(),
		Size:        1,
	}}
	attr.GidMappingsEnableSetgroups = false
	return attr
}

func sandboxChildArgv(argv []string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	childArgv := []string{exe, "-sandbox-child", "-workdir", *workdir}
	childArgv = appendSandboxLimitArgs(childArgv)
	if *staticExec {
		childArgv = append(childArgv, "-static-exec")
	}
	childArgv = append(childArgv, "--")
	childArgv = append(childArgv, argv...)
	return childArgv, nil
}

func appendSandboxLimitArgs(args []string) []string {
	if *cpuLimit > 0 {
		args = append(args, "-cpu", cpuLimit.String())
	}
	if *memLimit != "" && *cgroupRoot == "" {
		args = append(args, "-mem", *memLimit)
	}
	if *fileLimit != "" {
		args = append(args, "-fsize", *fileLimit)
	}
	if *openFiles > 0 {
		args = append(args, "-nofile", strconv.FormatUint(*openFiles, 10))
	}
	return args
}

func runSandboxChild(argv []string) int {
	if len(argv) == 0 {
		_, _ = os.Stderr.WriteString("sandbox child missing command\n")
		return 126
	}
	if err := setupFilesystemSandbox(argv, *workdir); err != nil {
		_, _ = os.Stderr.WriteString("sandbox filesystem: " + err.Error() + "\n")
		return 126
	}
	limits, err := buildRLimits()
	if err != nil {
		_, _ = os.Stderr.WriteString("limits: " + err.Error() + "\n")
		return 126
	}
	if err := applyRLimits(limits); err != nil {
		_, _ = os.Stderr.WriteString("limits: " + err.Error() + "\n")
		return 126
	}
	if err := os.Chdir(*workdir); err != nil {
		_, _ = os.Stderr.WriteString("chdir: " + err.Error() + "\n")
		return 126
	}
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		_, _ = os.Stderr.WriteString("exec: " + err.Error() + "\n")
		return 126
	}
	return 126
}

func setupFilesystemSandbox(argv []string, workdir string) error {
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		return err
	}
	return applyLandlock(argv, workdir)
}

const (
	landlockCreateRulesetVersion = 1
	landlockRulePathBeneath      = 1

	landlockAccessFSExecute    = uint64(1 << 0)
	landlockAccessFSWriteFile  = uint64(1 << 1)
	landlockAccessFSReadFile   = uint64(1 << 2)
	landlockAccessFSReadDir    = uint64(1 << 3)
	landlockAccessFSRemoveDir  = uint64(1 << 4)
	landlockAccessFSRemoveFile = uint64(1 << 5)
	landlockAccessFSMakeChar   = uint64(1 << 6)
	landlockAccessFSMakeDir    = uint64(1 << 7)
	landlockAccessFSMakeReg    = uint64(1 << 8)
	landlockAccessFSMakeSock   = uint64(1 << 9)
	landlockAccessFSMakeFifo   = uint64(1 << 10)
	landlockAccessFSMakeBlock  = uint64(1 << 11)
	landlockAccessFSMakeSym    = uint64(1 << 12)
	landlockAccessFSRefer      = uint64(1 << 13)
	landlockAccessFSTruncate   = uint64(1 << 14)
	landlockAccessFSIOCTLDev   = uint64(1 << 15)

	sysLandlockCreateRuleset = uintptr(444)
	sysLandlockAddRule       = uintptr(445)
	sysLandlockRestrictSelf  = uintptr(446)

	prSetNoNewPrivs = uintptr(38)
	oPath           = 010000000
)

type landlockRulesetAttr struct {
	HandledAccessFS uint64
}

type landlockPathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int32
}

func applyLandlock(argv []string, workdir string) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	handled := landlockHandledAccess(abi)
	ruleset := landlockRulesetAttr{HandledAccessFS: handled}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&ruleset)), unsafe.Sizeof(ruleset), 0)
	if errno != 0 {
		return errno
	}
	defer syscall.Close(int(fd))

	for _, rule := range collectLandlockPaths(argv, workdir, abi) {
		if err := addLandlockPathRule(int(fd), rule.path, rule.access&handled); err != nil {
			return err
		}
	}

	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

func landlockABI() (int, error) {
	abi, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, errno
	}
	if abi == 0 {
		return 0, errors.New("landlock ABI unavailable")
	}
	return int(abi), nil
}

func landlockHandledAccess(abi int) uint64 {
	access := landlockAccessFSExecute |
		landlockAccessFSWriteFile |
		landlockAccessFSReadFile |
		landlockAccessFSReadDir |
		landlockAccessFSRemoveDir |
		landlockAccessFSRemoveFile |
		landlockAccessFSMakeChar |
		landlockAccessFSMakeDir |
		landlockAccessFSMakeReg |
		landlockAccessFSMakeSock |
		landlockAccessFSMakeFifo |
		landlockAccessFSMakeBlock |
		landlockAccessFSMakeSym
	if abi >= 2 {
		access |= landlockAccessFSRefer
	}
	if abi >= 3 {
		access |= landlockAccessFSTruncate
	}
	if abi >= 5 {
		access |= landlockAccessFSIOCTLDev
	}
	return access
}

type landlockPathRule struct {
	path   string
	access uint64
}

func collectLandlockPaths(argv []string, workdir string, abi int) []landlockPathRule {
	rw := landlockReadAccess() | landlockWriteAccess(abi)
	ro := landlockReadAccess()
	rx := landlockReadAccess() | landlockAccessFSExecute

	rulesByPath := map[string]uint64{}
	add := func(path string, access uint64) {
		clean, ok := existingAbsolutePath(path)
		if !ok {
			return
		}
		rulesByPath[clean] |= access
	}

	add(workdir, rw)
	if len(argv) > 0 {
		add(argv[0], rx)
	}
	for _, path := range argvReadPaths(argv, workdir) {
		add(path, ro)
	}
	if !*staticExec {
		for _, path := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64"} {
			add(path, rx)
		}
	}

	rules := make([]landlockPathRule, 0, len(rulesByPath))
	for path, access := range rulesByPath {
		rules = append(rules, landlockPathRule{path: path, access: access})
	}
	return rules
}

func landlockReadAccess() uint64 {
	return landlockAccessFSReadFile | landlockAccessFSReadDir
}

func landlockWriteAccess(abi int) uint64 {
	access := landlockAccessFSWriteFile |
		landlockAccessFSRemoveDir |
		landlockAccessFSRemoveFile |
		landlockAccessFSMakeChar |
		landlockAccessFSMakeDir |
		landlockAccessFSMakeReg |
		landlockAccessFSMakeSock |
		landlockAccessFSMakeFifo |
		landlockAccessFSMakeBlock |
		landlockAccessFSMakeSym
	if abi >= 2 {
		access |= landlockAccessFSRefer
	}
	if abi >= 3 {
		access |= landlockAccessFSTruncate
	}
	if abi >= 5 {
		access |= landlockAccessFSIOCTLDev
	}
	return access
}

func argvReadPaths(argv []string, workdir string) []string {
	var paths []string
	skipNext := false
	for i, arg := range argv {
		if i == 0 {
			continue
		}
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-o" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-o=") {
			continue
		}
		if strings.HasPrefix(arg, "-importcfg=") {
			importcfg := strings.TrimPrefix(arg, "-importcfg=")
			paths = append(paths, importcfg)
			paths = append(paths, importcfgObjectPaths(importcfg)...)
			continue
		}
		if filepath.IsAbs(arg) && !pathWithin(arg, workdir) {
			paths = append(paths, arg)
		}
	}
	return paths
}

func importcfgObjectPaths(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if filepath.IsAbs(value) {
			paths = append(paths, value)
		}
	}
	return paths
}

func existingAbsolutePath(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	clean, err := filepath.EvalSymlinks(path)
	if err != nil {
		clean = filepath.Clean(path)
	}
	if _, err := os.Stat(clean); err != nil {
		return "", false
	}
	return clean, true
}

func pathWithin(path, root string) bool {
	cleanPath, ok := existingAbsolutePath(path)
	if !ok {
		cleanPath = filepath.Clean(path)
	}
	cleanRoot, ok := existingAbsolutePath(root)
	if !ok {
		cleanRoot = filepath.Clean(root)
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func addLandlockPathRule(rulesetFD int, path string, access uint64) error {
	if access == 0 {
		return nil
	}
	if st, err := os.Stat(path); err != nil {
		return err
	} else if !st.IsDir() {
		access &^= landlockAccessFSReadDir |
			landlockAccessFSRemoveDir |
			landlockAccessFSMakeChar |
			landlockAccessFSMakeDir |
			landlockAccessFSMakeReg |
			landlockAccessFSMakeSock |
			landlockAccessFSMakeFifo |
			landlockAccessFSMakeBlock |
			landlockAccessFSMakeSym
	}
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	rule := landlockPathBeneathAttr{
		AllowedAccess: access,
		ParentFD:      int32(fd),
	}
	_, _, errno := syscall.Syscall(sysLandlockAddRule, uintptr(rulesetFD), landlockRulePathBeneath, uintptr(unsafe.Pointer(&rule)))
	if errno != 0 {
		return errno
	}
	return nil
}

type rlimitSpec struct {
	resource int
	limit    syscall.Rlimit
}

func buildRLimits() ([]rlimitSpec, error) {
	var specs []rlimitSpec
	if *cpuLimit > 0 {
		seconds := uint64((*cpuLimit + time.Second - 1) / time.Second)
		if seconds == 0 {
			seconds = 1
		}
		specs = append(specs, rlimitSpec{
			resource: syscall.RLIMIT_CPU,
			limit:    syscall.Rlimit{Cur: seconds, Max: seconds},
		})
	}
	if *memLimit != "" && *cgroupRoot == "" {
		n, err := parseByteLimit(*memLimit)
		if err != nil {
			return nil, err
		}
		specs = append(specs, rlimitSpec{
			resource: syscall.RLIMIT_AS,
			limit:    syscall.Rlimit{Cur: n, Max: n},
		})
	}
	if *fileLimit != "" {
		n, err := parseByteLimit(*fileLimit)
		if err != nil {
			return nil, err
		}
		specs = append(specs, rlimitSpec{
			resource: syscall.RLIMIT_FSIZE,
			limit:    syscall.Rlimit{Cur: n, Max: n},
		})
	}
	if *openFiles > 0 {
		specs = append(specs, rlimitSpec{
			resource: syscall.RLIMIT_NOFILE,
			limit:    syscall.Rlimit{Cur: *openFiles, Max: *openFiles},
		})
	}
	return specs, nil
}

type commandCgroup struct {
	path string
}

func setupCgroup() (*commandCgroup, error) {
	if *cgroupRoot == "" {
		return nil, nil
	}
	root, err := filepath.Abs(*cgroupRoot)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(root); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, errors.New("cgroup root is not a directory")
	}
	name := "bplay-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0755); err != nil {
		return nil, err
	}
	cg := &commandCgroup{path: path}
	ok := false
	defer func() {
		if !ok {
			cg.cleanup()
		}
	}()
	if *memLimit != "" {
		n, err := parseByteLimit(*memLimit)
		if err != nil {
			return nil, err
		}
		if err := cg.write("memory.max", strconv.FormatUint(n, 10)); err != nil {
			return nil, err
		}
	}
	if *pidsMax > 0 {
		if err := cg.write("pids.max", strconv.FormatUint(*pidsMax, 10)); err != nil {
			return nil, err
		}
	}
	ok = true
	return cg, nil
}

func (c *commandCgroup) write(name, value string) error {
	return os.WriteFile(filepath.Join(c.path, name), []byte(value), 0644)
}

func (c *commandCgroup) addProcess(pid int) error {
	return c.write("cgroup.procs", strconv.Itoa(pid))
}

func (c *commandCgroup) kill() error {
	if err := c.write("cgroup.kill", "1"); err == nil {
		return nil
	}
	return nil
}

func (c *commandCgroup) cleanup() {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		if err := os.Remove(c.path); err == nil || os.IsNotExist(err) {
			return
		}
		for _, name := range []string{"memory.max", "pids.max", "cgroup.procs", "cgroup.kill", "memory.events", "pids.events"} {
			_ = os.Remove(filepath.Join(c.path, name))
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *commandCgroup) killReason() string {
	if c.eventValue("memory.events", "oom_kill") > 0 {
		return "oom"
	}
	if c.eventValue("pids.events", "max") > 0 {
		return "pids"
	}
	return ""
}

func (c *commandCgroup) eventValue(file, key string) uint64 {
	b, err := os.ReadFile(filepath.Join(c.path, file))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != key {
			continue
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		return n
	}
	return 0
}

func applyRLimits(specs []rlimitSpec) error {
	for _, spec := range specs {
		if err := syscall.Setrlimit(spec.resource, &spec.limit); err != nil {
			return err
		}
	}
	return nil
}

func parseByteLimit(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	mul := uint64(1)
	num := s
	for _, suffix := range []struct {
		s string
		m uint64
	}{
		{"KiB", 1 << 10},
		{"MiB", 1 << 20},
		{"GiB", 1 << 30},
		{"K", 1000},
		{"M", 1000 * 1000},
		{"G", 1000 * 1000 * 1000},
	} {
		if strings.HasSuffix(s, suffix.s) {
			mul = suffix.m
			num = strings.TrimSuffix(s, suffix.s)
			break
		}
	}
	n, err := strconv.ParseUint(strings.TrimSpace(num), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mul, nil
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func writeFatal(msg string) {
	res := runnerResult{
		ExitCode: -1,
		Stderr:   msg,
		Killed:   true,
		Reason:   "usage",
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
	os.Exit(2)
}

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

type limitBuffer struct {
	Limit     int
	Truncated bool
	buf       bytes.Buffer
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	limit := b.Limit
	if limit <= 0 {
		limit = 64 << 10
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
