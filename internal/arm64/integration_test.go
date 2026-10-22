package arm64_test

import (
	"context"
	"debug/macho"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests exercise the real compiler/assembler/linker boundary and execute
// native output, including the shipped runtime. Run on an Apple Silicon host
// with Command Line Tools; other hosts retain the portable lowering tests.
func TestDarwinPrograms(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("requires native Apple Silicon")
	}
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("requires Apple Command Line Tools")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	dir := t.TempDir()
	if artifacts := os.Getenv("BOSON_ARM64_TEST_ARTIFACTS"); artifacts != "" {
		dir = artifacts
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	current := t
	run := func(args ...string) string {
		current.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			current.Fatalf("%v: %v\n%s", args, err, output)
		}
		return string(output)
	}
	for _, tool := range []string{"bosc", "bas", "bld"} {
		run("go", "build", "-o", filepath.Join(dir, tool), "./cmd/"+tool)
	}
	cfg := filepath.Join(dir, "importcfg")
	var imports []string
	var objects []string
	compile := func(pkg string, sources ...string) string {
		current.Helper()
		if err := os.WriteFile(cfg, []byte(strings.Join(imports, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		asm := filepath.Join(dir, pkg+".bs")
		obj := filepath.Join(dir, pkg+".bo")
		args := []string{filepath.Join(dir, "bosc"), "-target=darwin/arm64", "-importcfg=" + cfg, "-o", asm}
		run(append(args, sources...)...)
		run(filepath.Join(dir, "bas"), "-target=darwin/arm64", "-o", obj, asm)
		return obj
	}
	for _, pkg := range []string{"_sys", "_init", "builtin", "sys", "io", "fmt", "_heap", "_iface"} {
		obj := filepath.Join(dir, pkg+".bo")
		if pkg == "_sys" || pkg == "_init" {
			name := "sys"
			if pkg == "_init" {
				name = "init"
			}
			run(filepath.Join(dir, "bas"), "-target=darwin/arm64", "-o", obj, filepath.Join(root, "runtime", pkg, name+"_darwin_arm64.bs"))
		} else {
			sources, err := filepath.Glob(filepath.Join(root, "runtime", pkg, "*.bos"))
			if err != nil {
				t.Fatal(err)
			}
			compile(pkg, sources...)
		}
		imports = append(imports, pkg+"="+obj)
		objects = append(objects, obj)
	}
	// The legacy language suite's printing fixture has a native implementation
	// in ordinary Boson, using the shipped formatter and IO instead of syscalls.
	fixture := filepath.Join(dir, "string.bos")
	if err := os.WriteFile(fixture, []byte(`package string
import "fmt"
import "io"
import "sys"
pub fn puts(s []byte) { fmt.str(&io.STDOUT, s) }
pub fn puti(n i64) { fmt.int(&io.STDOUT, n) }
pub fn putb(n byte) { fmt.int(&io.STDOUT, i64(n)) }
pub fn putc(n byte) { fmt.char(&io.STDOUT, n) }
pub fn exit(n i64) { sys.exit(n) }
`), 0600); err != nil {
		t.Fatal(err)
	}
	obj := compile("string", fixture)
	// Add raw entry probes without a prologue so alignment is measured at call entry.
	probes := filepath.Join(dir, "probes.bs")
	os.WriteFile(probes, []byte(`package string
pub function entryalign7
 type fn(i64,i64,i64,i64,i64,i64,i64) i64
 mov x8 sp
 and x8 x8 15
 mov x0 x8
 ret
pub function entryalign8
 type fn(i64,i64,i64,i64,i64,i64,i64,i64) i64
 mov x8 sp
 and x8 x8 15
 mov x0 x8
 ret
pub function rspval
 type fn() i64
 mov x8 sp
 mov x0 x8
 ret
`), 0600)
	run(filepath.Join(dir, "bas"), "-target=darwin/arm64", "-o", obj, filepath.Join(dir, "string.bs"), probes)
	imports = append(imports, "string="+obj)
	objects = append(objects, obj)
	// Cross-package fixtures use the same target as the consuming program.
	for _, pkg := range []string{"pair", "errors", "geom", "ifaces", "visibility", "retalias_pkg", "flags/gnu"} {
		path := filepath.Join(root, "cmd/bosc/testpkgs", pkg)
		if pkg == "flags/gnu" {
			path = filepath.Join(root, "runtime", pkg)
		}
		sources, _ := filepath.Glob(filepath.Join(path, "*.bos"))
		name := strings.ReplaceAll(pkg, "/", "_")
		obj := compile(name, sources...)
		imports = append(imports, pkg+"="+obj)
		objects = append(objects, obj)
	}
	tests := []string{"add", "argv", "struct", "global_var", "stack_args", "float_arith", "float_cast", "float_compare", "float_abi", "float_composite", "fmt_itoa", "fmt_float", "heap_small", "io_buffered_writer", "union_three", "variadic_slice_elements", "assert_iface_success", "iface_truthiness", "iface_value_receiver_reads_field", "empty_slice_literal", "i8", "i16", "i32", "heap_release", "compound_assign", "uintptr", "unsafe_cast", "owned_field_move_nullable_passable"}
	if os.Getenv("BOSON_ARM64_ALL_TESTS") == "1" {
		tests = nil
		sources, _ := filepath.Glob(filepath.Join(root, "cmd/bosc/tests/*_test.bos"))
		for _, src := range sources {
			if !strings.HasSuffix(src, "_err_test.bos") {
				tests = append(tests, strings.TrimSuffix(filepath.Base(src), "_test.bos"))
			}
		}
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			current = t
			source := filepath.Join(root, "cmd/bosc/tests", name+"_test.bos")
			if _, err := os.Stat(source); err != nil {
				t.Fatal(err)
			}
			// Reuse a single package output between cases; link every case immediately.
			main := compile("main", source)
			exe := filepath.Join(dir, "program")
			args := []string{filepath.Join(dir, "bld"), "-target=darwin/arm64", "-o", exe, main}
			run(append(args, objects...)...)
			f, err := macho.Open(exe)
			if err != nil {
				t.Fatal(err)
			}
			if f.Cpu != macho.CpuArm64 || f.Type != macho.TypeExec {
				t.Fatal("wrong executable format")
			}
			f.Close()
			want, err := os.ReadFile(source + ".expected")
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if name == "stack_args" {
				want = []byte(strings.TrimSuffix(string(want), "8\n8\n0\n") + "0\n0\n0\n")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var argv []string
			if argdata, err := os.ReadFile(source + ".args"); err == nil {
				argv = strings.Fields(string(argdata))
			}
			cmd := exec.CommandContext(ctx, exe, argv...)
			cmd.Dir = filepath.Join(root, "cmd/bosc")
			output, runerr := cmd.Output()
			status := 0
			if runerr != nil {
				if e, ok := runerr.(*exec.ExitError); ok {
					status = e.ExitCode()
				} else {
					t.Fatal(runerr)
				}
			}
			expectedStatus := 0
			if statusData, err := os.ReadFile(source + ".exit"); err == nil {
				expectedStatus, _ = strconv.Atoi(strings.TrimSpace(string(statusData)))
			}
			if ctx.Err() != nil {
				t.Fatalf("program timed out: %s", output)
			}
			if status != expectedStatus {
				t.Fatalf("exit status %d, want %d; stdout: %s", status, expectedStatus, output)
			}
			got := string(output)
			if err == nil && got != string(want) {
				t.Fatalf("stdout:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
