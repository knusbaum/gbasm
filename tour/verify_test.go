package tourcontent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/knusbaum/gbasm/internal/bpipeline"
	"github.com/knusbaum/gbasm/internal/tour"
)

// TestLessonsLoad validates the embedded lesson tree (front matter, required
// files, check schema) without needing the toolchain. It always runs in CI.
func TestLessonsLoad(t *testing.T) {
	cat, err := tour.Load(FS())
	if err != nil {
		t.Fatalf("load lessons: %v", err)
	}
	if len(cat.Sections) == 0 {
		t.Fatal("no sections loaded")
	}
	for _, sec := range cat.Sections {
		for _, l := range sec.Lessons {
			if l.Title == "" {
				t.Errorf("%s/%s: empty title", l.Section, l.Slug)
			}
			// Runnable lessons must carry a program; prose-only lessons
			// (intros/overviews with no main.bos) intentionally have none.
			if l.HasSource && l.Source == "" {
				t.Errorf("%s/%s: empty source", l.Section, l.Slug)
			}
			if !l.HasSource && l.Source != "" {
				t.Errorf("%s/%s: prose-only lesson has source", l.Section, l.Slug)
			}
		}
	}
}

// TestLessonsRun compiles and runs every lesson through the real pipeline and
// applies its check. It skips when the toolchain binaries or runtime bundle
// have not been built (mirrors cmd/bplayd's end-to-end tests).
func TestLessonsRun(t *testing.T) {
	toolchain := filepath.Clean("..")
	for _, name := range []string{"bosc", "bas", "bld"} {
		if _, err := os.Stat(filepath.Join(toolchain, name)); err != nil {
			t.Skipf("toolchain binary %s not built", name)
		}
	}
	bundleRoot := filepath.Join(toolchain, "target", "playground")
	if _, err := bpipeline.ValidateRuntimeBundle(bundleRoot); err != nil {
		t.Skipf("playground runtime bundle not built: %v", err)
	}

	pipe, err := bpipeline.New(bpipeline.Config{
		ToolchainDir:  toolchain,
		RuntimeBundle: bundleRoot,
		Timeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	cat, err := tour.Load(FS())
	if err != nil {
		t.Fatalf("load lessons: %v", err)
	}

	for _, sec := range cat.Sections {
		for _, l := range sec.Lessons {
			t.Run(l.Section+"/"+l.Slug, func(t *testing.T) {
				if !l.HasSource {
					t.Skip("prose-only lesson: nothing to run")
				}
				resp := pipe.Run(context.Background(), bpipeline.Request{Source: l.Source})
				res := l.Verify(resp)
				if !res.Passed {
					t.Fatalf("check failed: %s", res.Message)
				}
			})
		}
	}
}
