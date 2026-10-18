package gbasm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBosonMultiBinaryPackages(t *testing.T) {
	mmk := "mmk"
	if _, err := exec.LookPath(mmk); err != nil {
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
	project := t.TempDir()
	files := map[string]string{
		"mmkfile": fmt.Sprintf(`BOSON_HOME=%s
BOSC=%s/bosc
BAS=%s/bas
BLD=%s/bld
BOSONPATH=shared
include %s/boson.mmk
all : cat wc
bos_exe cat source=commands/cat :
bos_exe wc source=commands/wc :
`, repo, repo, repo, repo, repo),
		"shared/whatever/base.bos":      "package whatever\npub fn base() i64 { return 10 }\n",
		"shared/coreutil/args/args.bos": "package args\nimport \"whatever\"\npub fn value() i64 { return whatever.base() + 1 }\n",
		"commands/cat/main.bos":         "package main\nimport \"coreutil/args\"\nimport \"cathelper\"\nimport \"fmt\"\nfn main() { fmt.print(\"%d\\n\", args.value()+cathelper.value()) }\n",
		"shared/cathelper/local.bos":    "package cathelper\npub fn value() i64 { return 20 }\n",
		"commands/wc/main.bos":          "package main\nimport \"coreutil/args\"\nimport \"fmt\"\nfn main() { fmt.print(\"%d\\n\", args.value()) }\n",
	}
	for name, contents := range files {
		path := filepath.Join(project, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	build := func(target string) string {
		t.Helper()
		cmd := exec.Command(mmk, "-j", "4", target)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mmk %s: %v\n%s", target, err, out)
		}
		return string(out)
	}
	check := func(name, want string) {
		t.Helper()
		out, err := exec.Command(filepath.Join(project, "target", name)).CombinedOutput()
		if err != nil || string(out) != want {
			t.Fatalf("%s: %q (%v), want %q", name, out, err, want)
		}
	}
	build("all")
	check("cat", "31\n")
	check("wc", "11\n")
	for _, path := range []string{"target/whatever.bo", "target/coreutil/args.bo", "target/cathelper.bo"} {
		if _, err := os.Stat(filepath.Join(project, path)); err != nil {
			t.Fatal(err)
		}
	}
	if out := build("all"); strings.Contains(out, "Compiling ") {
		t.Fatalf("unchanged packages rebuilt:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(project, "shared/whatever/base.bos"), []byte("package whatever\npub fn base() i64 { return 12 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	build("all")
	check("cat", "33\n")
	check("wc", "13\n")
}

func TestBosonRuntimeSearchPath(t *testing.T) {
	if _, err := exec.LookPath("mmk"); err != nil {
		t.Skip("mmk is not installed")
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, path, packageDir string }{
		{"default", "", "./whatever"},
		{"custom root", "lib", "lib/whatever"},
		{"multiple roots", "missing:lib", "lib/whatever"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			for _, dir := range []string{"whatever", "lib/whatever", "lib/io", "io"} {
				if err := os.MkdirAll(filepath.Join(project, dir), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(project, "mmkfile"), []byte("include "+repo+"/boson.mmk\nall :\n"), 0644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("mmk", "-dump")
			cmd.Dir = project
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "BOSONPATH=") && !strings.HasPrefix(entry, "BOSON_HOME=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "BOSON_HOME="+repo, "BOSONPATH="+tt.path)
			dump, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("mmk -dump: %v\n%s", err, dump)
			}
			file := filepath.Join(project, "generated.sh")
			if err := os.WriteFile(file, dump, 0600); err != nil {
				t.Fatal(err)
			}
			check := exec.Command("bash", "-c", `. "$1"; resolve_pkg io; resolve_pkg whatever`, "--", file)
			check.Dir = project
			check.Env = cmd.Env
			out, err := check.CombinedOutput()
			want := filepath.Join(repo, "runtime/io") + "\n" + tt.packageDir + "\n"
			if err != nil || string(out) != want {
				t.Fatalf("got %q (%v), want %q", out, err, want)
			}
		})
	}
}
