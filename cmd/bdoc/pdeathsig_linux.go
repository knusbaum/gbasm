//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

// installParentDeathSignal arranges for the kernel to send SIGTERM to
// this process if its parent exits. The intended use is the dev-server
// scenario where bdoc is launched by an mmk target (or any other
// supervisor) that may itself exit on Ctrl-C without propagating the
// signal; without this, bdoc would be reparented to PID 1 and keep
// running while still holding the listen port. With it, the parent's
// death triggers a SIGTERM that the Go runtime's default handler
// translates into a clean exit.
//
// Linux-specific (prctl(PR_SET_PDEATHSIG, ...)). The bdoc init() below
// is the only mention; non-Linux builds get a no-op sibling file.
func init() {
	// syscall.Prctl isn't exported; go through RawSyscall6 with the
	// well-known prctl(2) number and the PR_SET_PDEATHSIG operation.
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0, 0, 0, 0)
	if errno != 0 {
		fmt.Fprintf(os.Stderr, "bdoc: PR_SET_PDEATHSIG failed: %v (continuing without parent-death fallback)\n", errno)
	}
}
