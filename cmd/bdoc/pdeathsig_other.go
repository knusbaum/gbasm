//go:build !linux

package main

// No PR_SET_PDEATHSIG equivalent on non-Linux platforms; the Linux
// sibling file owns the active behavior.
