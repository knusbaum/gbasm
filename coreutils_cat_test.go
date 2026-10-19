package gbasm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Build the actual example in isolation so it exercises the shipped caller,
// transformation filter, and buffered writer together.
func TestCoreutilsCat(t *testing.T) {
	if _, err := exec.LookPath("mmk"); err != nil {
		t.Skip("mmk is not installed")
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"bosc", "bas", "bld"} {
		if _, err := os.Stat(filepath.Join(repo, tool)); err != nil {
			t.Skip("run mmk go_test to build the toolchain")
		}
	}
	dir := t.TempDir()
	source, err := os.ReadFile("examples/coreutils/src/cat/main.bos")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.bos"), source, 0644); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("BOSON_HOME=%s\nBOSC=%s/bosc\nBAS=%s/bas\nBLD=%s/bld\ninclude %s/boson.mmk\nall : cat\nbos_exe cat source=. :\n", repo, repo, repo, repo, repo)
	if err := os.WriteFile(filepath.Join(dir, "mmkfile"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("mmk")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cat: %v\n%s", err, out)
	}
	binary := filepath.Join(dir, "target/cat")
	run := func(t *testing.T, exe string, args []string, input []byte) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("%s %q: %v\n%s", exe, args, err, stderr.Bytes())
		}
		return out
	}
	for _, tc := range []struct {
		name        string
		args        []string
		input, want string
	}{
		{"number", []string{"-n"}, "a\n\nb", "     1\ta\n     2\t\n     3\tb"},
		{"nonblank overrides number", []string{"-bn"}, "\na\n\n", "\n     1\ta\n\n"},
		{"squeeze before numbering", []string{"-sn"}, "\n\na\n\n\n", "     1\t\n     2\ta\n     3\t\n"},
		{"meta and control bytes", []string{"-A"}, "\x00\t\x7f\x80\x89\x8a\xa0\xff\r\n", "^@^I^?M-^@M-^IM-^JM- M-^?^M$\n"},
		{"CRLF and trailing CR", []string{"-E"}, "\rX\r\n\r", "\rX^M$\n\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, binary, tc.args, []byte(tc.input)); !bytes.Equal(got, []byte(tc.want)) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("final flush error", func(t *testing.T) {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Skip("requires /dev/full")
		}
		defer full.Close()
		cmd := exec.Command(binary, "-E")
		cmd.Stdin = strings.NewReader("x\n")
		cmd.Stdout = full
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			t.Fatal("expected a failed final flush")
		}
		if stderr.Len() == 0 {
			t.Fatal("expected output error diagnostic")
		}
	})
	t.Run("GNU oracle", func(t *testing.T) {
		reference, err := exec.LookPath("cat")
		if err != nil {
			t.Skip("GNU cat is not installed")
		}
		version, err := exec.Command(reference, "--version").Output()
		if err != nil || !bytes.Contains(version, []byte("GNU coreutils")) {
			t.Skip("oracle requires GNU cat")
		}
		payload := []byte("\n\na\t\r\n\n\nb\r")
		for i := 0; i < 256; i++ {
			payload = append(payload, byte(i))
		}
		payload = append(payload, bytes.Repeat([]byte("long\tline\x80\n\n\n"), 500)...)
		// Place CRLF exactly across io.copy's 1024-byte read boundary.
		payload = append(bytes.Repeat([]byte("x"), 1023), append([]byte("\r\n"), payload...)...)
		payload = append(payload, '\r')
		for mask := 0; mask < 64; mask++ {
			flags := "-"
			for i, c := range "bnsvET" {
				if mask&(1<<i) != 0 {
					flags += string(c)
				}
			}
			var args []string
			if flags != "-" {
				args = []string{flags}
			}
			if got, want := run(t, binary, args, payload), run(t, reference, args, payload); !bytes.Equal(got, want) {
				t.Fatalf("%v output differs from GNU cat", args)
			}
		}
		for _, flag := range []string{"-A", "-e", "-t", "-u", "--number-nonblank", "--show-all"} {
			args := []string{flag}
			if got, want := run(t, binary, args, payload), run(t, reference, args, payload); !bytes.Equal(got, want) {
				t.Fatalf("%s output differs from GNU cat", flag)
			}
		}
		left := filepath.Join(dir, "left")
		right := filepath.Join(dir, "right")
		if err := os.WriteFile(left, []byte("\n\na\r"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(right, []byte("\n\n\nb\n\n"), 0644); err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"-E", "-ns", "-bns", "-A"} {
			for _, operands := range [][]string{{left, right}, {left, "-", right}} {
				args := append([]string{flag}, operands...)
				input := []byte("\n\nfrom stdin")
				if got, want := run(t, binary, args, input), run(t, reference, args, input); !bytes.Equal(got, want) {
					t.Fatalf("%v output differs from GNU cat: got %q want %q", args, got, want)
				}
			}
		}
	})
}
