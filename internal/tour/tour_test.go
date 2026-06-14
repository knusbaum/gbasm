package tour

import (
	"testing"
	"testing/fstest"

	"github.com/knusbaum/gbasm/internal/bpipeline"
)

func sampleFS() fstest.MapFS {
	return fstest.MapFS{
		"01-basics/01-hello/lesson.md":       {Data: []byte("---\ntitle: Hello, Boson\nsection: Basics\norder: 1\n---\nWelcome.\n")},
		"01-basics/01-hello/main.bos":        {Data: []byte("package main\n")},
		"01-basics/01-hello/expected.stdout": {Data: []byte("hello\n")},

		"01-basics/02-vars/lesson.md":       {Data: []byte("---\ntitle: Variables\nsection: Basics\n---\nVars.\n")},
		"01-basics/02-vars/main.bos":        {Data: []byte("package main\n")},
		"01-basics/02-vars/expected.stdout": {Data: []byte("")},

		"02-ownership/01-use-after-move/lesson.md":  {Data: []byte("---\ntitle: Use After Move\nsection: Ownership\n---\nBad.\n")},
		"02-ownership/01-use-after-move/main.bos":   {Data: []byte("package main\n")},
		"02-ownership/01-use-after-move/check.json": {Data: []byte(`{"kind":"diagnostic","contains":["consumed"]}`)},
	}
}

func TestLoadOrdersSectionsAndLessons(t *testing.T) {
	cat, err := Load(sampleFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cat.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(cat.Sections))
	}
	if cat.Sections[0].Name != "Basics" || cat.Sections[1].Name != "Ownership" {
		t.Fatalf("section names = %q, %q", cat.Sections[0].Name, cat.Sections[1].Name)
	}
	basics := cat.Sections[0]
	if len(basics.Lessons) != 2 {
		t.Fatalf("basics lessons = %d, want 2", len(basics.Lessons))
	}
	if basics.Lessons[0].Slug != "01-hello" || basics.Lessons[1].Slug != "02-vars" {
		t.Fatalf("lesson order = %q, %q", basics.Lessons[0].Slug, basics.Lessons[1].Slug)
	}
	if got := basics.Lessons[0].Prose; got != "Welcome.\n" {
		t.Fatalf("prose = %q", got)
	}
}

func TestLookupAndDiagnosticFlag(t *testing.T) {
	cat, err := Load(sampleFS())
	if err != nil {
		t.Fatal(err)
	}
	l, ok := cat.Lookup("02-ownership", "01-use-after-move")
	if !ok {
		t.Fatal("lookup failed")
	}
	if !l.diagnostic() {
		t.Fatal("expected diagnostic lesson")
	}
}

func TestLoadRejectsMissingTitle(t *testing.T) {
	fs := fstest.MapFS{
		"01-basics/01-hello/lesson.md":       {Data: []byte("---\nsection: Basics\n---\nhi\n")},
		"01-basics/01-hello/main.bos":        {Data: []byte("package main\n")},
		"01-basics/01-hello/expected.stdout": {Data: []byte("")},
	}
	if _, err := Load(fs); err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestLoadRejectsRunnableWithoutExpected(t *testing.T) {
	fs := fstest.MapFS{
		"01-basics/01-hello/lesson.md": {Data: []byte("---\ntitle: Hi\n---\nhi\n")},
		"01-basics/01-hello/main.bos":  {Data: []byte("package main\n")},
	}
	if _, err := Load(fs); err == nil {
		t.Fatal("expected error for runnable lesson without expected.stdout")
	}
}

func TestVerifyStdout(t *testing.T) {
	cat, _ := Load(sampleFS())
	l, _ := cat.Lookup("01-basics", "01-hello")

	ok := l.Verify(bpipeline.Response{
		Status:  bpipeline.StatusOK,
		Program: bpipeline.ProgramResult{Stdout: "hello\n"},
	})
	if !ok.Passed {
		t.Fatalf("expected pass, got %q", ok.Message)
	}

	bad := l.Verify(bpipeline.Response{
		Status:  bpipeline.StatusOK,
		Program: bpipeline.ProgramResult{Stdout: "nope\n"},
	})
	if bad.Passed {
		t.Fatal("expected stdout mismatch to fail")
	}

	failed := l.Verify(bpipeline.Response{
		Status: bpipeline.StatusCompileError,
		Steps:  []bpipeline.StepResult{{Name: "bosc", ExitCode: 1, Stderr: "boom"}},
	})
	if failed.Passed {
		t.Fatal("expected non-ok run to fail a stdout lesson")
	}
}

func TestVerifyDiagnostic(t *testing.T) {
	cat, _ := Load(sampleFS())
	l, _ := cat.Lookup("02-ownership", "01-use-after-move")

	ok := l.Verify(bpipeline.Response{
		Status: bpipeline.StatusCompileError,
		Steps:  []bpipeline.StepResult{{Name: "bosc", ExitCode: 1, Stderr: "value was consumed here"}},
	})
	if !ok.Passed {
		t.Fatalf("expected diagnostic pass, got %q", ok.Message)
	}

	wrongMsg := l.Verify(bpipeline.Response{
		Status: bpipeline.StatusCompileError,
		Steps:  []bpipeline.StepResult{{Name: "bosc", ExitCode: 1, Stderr: "some other error"}},
	})
	if wrongMsg.Passed {
		t.Fatal("expected diagnostic to fail on wrong message")
	}

	ran := l.Verify(bpipeline.Response{Status: bpipeline.StatusOK})
	if ran.Passed {
		t.Fatal("expected diagnostic to fail when compile succeeds")
	}
}
