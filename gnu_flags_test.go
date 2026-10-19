package gbasm

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the shipped package through the real compiler, assembler, and
// linker. The optional libc oracle checks parsing semantics, not diagnostics.
func TestGNUFlags(t *testing.T) {
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
	project := t.TempDir()
	driver, err := os.ReadFile("testdata/gnu_flags/driver.bos")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.bos"), driver, 0644); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("BOSON_HOME=%s\nBOSC=%s/bosc\nBAS=%s/bas\nBLD=%s/bld\ninclude %s/boson.mmk\nall : driver\nbos_exe driver source=. :\n", repo, repo, repo, repo, repo)
	if err := os.WriteFile(filepath.Join(project, "mmkfile"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("mmk")
	build.Dir = project
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build parser driver: %v\n%s", err, out)
	}
	binary := filepath.Join(project, "target/driver")
	run := func(t *testing.T, binary, shorts string, longOnly, posix bool, args []string) string {
		t.Helper()
		boolArg := func(b bool) string {
			if b {
				return "1"
			}
			return "0"
		}
		commandArgs := []string{shorts, boolArg(longOnly), boolArg(posix), "prog"}
		commandArgs = append(commandArgs, args...)
		out, err := exec.Command(binary, commandArgs...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %q: %v\n%s", binary, commandArgs, err, out)
		}
		return string(out)
	}
	for _, tc := range []struct {
		name, shorts string
		args         []string
		want         string
	}{
		{"cluster", "tFVu", []string{"-tFVu"}, "116|false||0|-1|0\n70|false||0|-1|0\n86|false||0|-1|0\n117|false||0|-1|0\nend|2|0\n"},
		{"empty and required arguments", "ab:c::", []string{"--beta=", "--color", "--color=blue", "-b", "--", "-a", "tail"}, "98|true||0|2|0\n99|false||0|3|0\n99|true|blue|0|3|0\n98|true|--|0|-1|0\n97|false||0|-1|0\nend|7|0\nrest|tail\n"},
		{"stable operands", "ab:", []string{"left", "-a", "middle", "--beta", "B", "-", "right", "--", "--alpha"}, "97|false||0|-1|0\n98|true|B|0|2|0\nend|5|0\nrest|left\nrest|middle\nrest|-\nrest|right\nrest|--alpha\n"},
		{"recoverable errors", ":a", []string{"-az", "--al", "--alpha=bad", "--beta"}, "97|false||0|-1|0\n63|false||122|-1|0\n63|false||0|-1|0\n63|false||97|-1|0\n58|false||98|-1|0\nend|5|0\n"},
		{"optional arguments", "o::", []string{"-o", "file", "--optional", "next", "--optional=", "-ox"}, "111|false||0|-1|0\n111|false||0|10|0\n111|true||0|10|0\n111|true|x|0|-1|0\nend|5|0\nrest|file\nrest|next\n"},
		{"POSIX order", "+a", []string{"file", "-a"}, "end|1|0\nrest|file\nrest|-a\n"},
		{"return in order", "-a", []string{"file", "-a", "tail"}, "1|true|file|0|-1|0\n97|false||0|-1|0\n1|true|tail|0|-1|0\nend|4|0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, binary, tc.shorts, false, false, tc.args); got != tc.want {
				t.Fatalf("got:\n%swant:\n%s", got, tc.want)
			}
		})
	}
	t.Run("glibc", func(t *testing.T) {
		cc, err := exec.LookPath("cc")
		if err != nil {
			t.Skip("optional libc reference requires cc")
		}
		// This is intentionally an external test-only program, never linked into
		// Boson programs and never a dependency of the parser itself.
		reference := filepath.Join(project, "reference")
		if out, err := exec.Command(cc, "-O2", "-o", reference, filepath.Join(repo, "testdata/gnu_flags/reference.c")).CombinedOutput(); err != nil {
			t.Fatalf("reference: %v\n%s", err, out)
		}
		// Reject non-GNU libc oracles, whose getopt extensions intentionally differ.
		if out, err := exec.Command("ldd", "--version").CombinedOutput(); err != nil || (!strings.Contains(string(out), "GLIBC") && !strings.Contains(string(out), "GNU libc")) {
			t.Skip("reference requires GNU libc")
		}
		cases := [][]string{
			{}, {""}, {"-"}, {"--"}, {"-tFVu"}, {"-abVALUE"}, {"-b", "-a"}, {"-c", "value"},
			{"--alpha"}, {"--al"}, {"--alph"}, {"--syn"}, {"--synonym"}, {"--synonyms"},
			{"--beta"}, {"--beta="}, {"--beta", "--"}, {"--alpha="}, {"--color", "next"},
			{"--color="}, {"--=value"}, {"-foo"}, {"-fo"}, {"-f"}, {"-f1"}, {"-unknown"},
			{"left", "-abVALUE", "middle", "--verbose", "right", "--", "-a"},
			{"-Walpha"}, {"-W", "beta=value"}, {"-W", "beta", "value"}, {"-W"}, {"-W", "unknown"},
			{"-W", "alpha=x"}, {"-W", "al"}, {"-Wcolor="}, {"-W", "--"},
		}
		tokens := []string{"file", "", "-", "--", "-a", "-b", "-bx", "-c", "-c=", "-o", "-r", "-az", "-tFVu", "--alpha", "--al", "--verbose", "--version", "--beta", "--beta=", "--color", "--color=red", "--syn", "-foo", "-W", "beta=x", "-Wbeta=x", "--unknown"}
		random := rand.New(rand.NewSource(42))
		for i := 0; i < 120; i++ {
			args := make([]string, random.Intn(9))
			for j := range args {
				args[j] = tokens[random.Intn(len(tokens))]
			}
			cases = append(cases, args)
		}
		for _, shorts := range []string{"ab:c::o::r:fW;tFVu", ":ab:c::o::r:f:W;tFVu", "+ab:c::o::r:fW;tFVu", "-ab:c::o::r:fW;tFVu", ""} {
			for _, longOnly := range []bool{false, true} {
				for _, posix := range []bool{false, true} {
					for _, args := range cases {
						want := run(t, reference, shorts, longOnly, posix, args)
						got := run(t, binary, shorts, longOnly, posix, args)
						if got != want {
							t.Fatalf("shorts=%q long_only=%v posix=%v argv=%q\ngot:\n%swant:\n%s", shorts, longOnly, posix, args, got, want)
						}
					}
				}
			}
		}
	})
}
