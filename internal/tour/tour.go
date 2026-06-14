// Package tour loads and verifies Boson tour lessons. A lesson is a directory
// of plain files (prose with front matter, a source program, expected output,
// and an optional check) under a section directory:
//
//	<section>/<lesson>/
//	    lesson.md          front matter + prose
//	    main.bos           the editable program
//	    expected.stdout    expected program output (runnable lessons)
//	    check.json         optional check override
//
// The loader is filesystem-agnostic (it takes an fs.FS), so callers can load
// from an embedded FS in the server or from disk in a verifier. Verify applies
// a lesson's check to a bpipeline.Response.
package tour

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/knusbaum/gbasm/internal/bpipeline"
)

// Check describes how a lesson's run is judged. Kind "stdout" (the default for
// runnable lessons) expects a successful run; Kind "diagnostic" expects the
// program to fail to compile with a message containing the listed substrings.
type Check struct {
	Kind     string   `json:"kind"`
	Contains []string `json:"contains,omitempty"`
	Equals   *string  `json:"equals,omitempty"`
}

// Lesson is a single loaded tour lesson.
type Lesson struct {
	Section     string // section directory name, e.g. "01-basics"
	Slug        string // lesson directory name, e.g. "01-hello"
	Title       string // front matter title
	SectionName string // front matter section display name
	Order       int    // sort order within the section
	Prose       string // lesson.md body (after front matter)
	Source      string // main.bos
	Expected    string // expected.stdout (when HasExpected)
	HasExpected bool
	Check       *Check // check.json, if present
}

// SectionGroup is an ordered group of lessons sharing a section directory.
type SectionGroup struct {
	Dir     string    // section directory name
	Name    string    // display name
	Order   int       // sort order among sections
	Lessons []*Lesson // ordered
}

// Catalog is the loaded, ordered set of lessons.
type Catalog struct {
	Sections []*SectionGroup
	byKey    map[string]*Lesson
}

// Lookup returns the lesson at section/slug.
func (c *Catalog) Lookup(section, slug string) (*Lesson, bool) {
	l, ok := c.byKey[section+"/"+slug]
	return l, ok
}

// Load reads every lesson from fsys, validates required files and front
// matter, and returns an ordered Catalog.
func Load(fsys fs.FS) (*Catalog, error) {
	sectionEntries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read tour root: %w", err)
	}
	cat := &Catalog{byKey: make(map[string]*Lesson)}
	for _, se := range sectionEntries {
		if !se.IsDir() {
			continue
		}
		grp := &SectionGroup{Dir: se.Name(), Order: leadingNumber(se.Name()), Name: humanize(se.Name())}
		lessonEntries, err := fs.ReadDir(fsys, se.Name())
		if err != nil {
			return nil, fmt.Errorf("read section %s: %w", se.Name(), err)
		}
		for _, le := range lessonEntries {
			if !le.IsDir() {
				continue
			}
			lesson, err := loadLesson(fsys, se.Name(), le.Name())
			if err != nil {
				return nil, err
			}
			if lesson.SectionName != "" {
				grp.Name = lesson.SectionName
			}
			grp.Lessons = append(grp.Lessons, lesson)
			cat.byKey[lesson.Section+"/"+lesson.Slug] = lesson
		}
		if len(grp.Lessons) == 0 {
			continue
		}
		sort.SliceStable(grp.Lessons, func(i, j int) bool {
			if grp.Lessons[i].Order != grp.Lessons[j].Order {
				return grp.Lessons[i].Order < grp.Lessons[j].Order
			}
			return grp.Lessons[i].Slug < grp.Lessons[j].Slug
		})
		cat.Sections = append(cat.Sections, grp)
	}
	sort.SliceStable(cat.Sections, func(i, j int) bool {
		if cat.Sections[i].Order != cat.Sections[j].Order {
			return cat.Sections[i].Order < cat.Sections[j].Order
		}
		return cat.Sections[i].Dir < cat.Sections[j].Dir
	})
	return cat, nil
}

func loadLesson(fsys fs.FS, section, slug string) (*Lesson, error) {
	dir := path.Join(section, slug)
	md, err := fs.ReadFile(fsys, path.Join(dir, "lesson.md"))
	if err != nil {
		return nil, fmt.Errorf("lesson %s/%s: missing lesson.md: %w", section, slug, err)
	}
	front, prose, err := parseFrontMatter(string(md))
	if err != nil {
		return nil, fmt.Errorf("lesson %s/%s: %w", section, slug, err)
	}
	src, err := fs.ReadFile(fsys, path.Join(dir, "main.bos"))
	if err != nil {
		return nil, fmt.Errorf("lesson %s/%s: missing main.bos: %w", section, slug, err)
	}

	lesson := &Lesson{
		Section:     section,
		Slug:        slug,
		Title:       front["title"],
		SectionName: front["section"],
		Order:       leadingNumber(slug),
		Prose:       prose,
		Source:      string(src),
	}
	if lesson.Title == "" {
		return nil, fmt.Errorf("lesson %s/%s: front matter is missing 'title'", section, slug)
	}
	if v, ok := front["order"]; ok {
		n, err := parseInt(v)
		if err != nil {
			return nil, fmt.Errorf("lesson %s/%s: bad 'order' %q: %w", section, slug, v, err)
		}
		lesson.Order = n
	}

	if cj, err := fs.ReadFile(fsys, path.Join(dir, "check.json")); err == nil {
		var c Check
		if err := json.Unmarshal(cj, &c); err != nil {
			return nil, fmt.Errorf("lesson %s/%s: bad check.json: %w", section, slug, err)
		}
		if c.Kind != "stdout" && c.Kind != "diagnostic" {
			return nil, fmt.Errorf("lesson %s/%s: check.json kind %q must be \"stdout\" or \"diagnostic\"", section, slug, c.Kind)
		}
		lesson.Check = &c
	}

	if exp, err := fs.ReadFile(fsys, path.Join(dir, "expected.stdout")); err == nil {
		lesson.Expected = string(exp)
		lesson.HasExpected = true
	}

	// A runnable lesson must declare its expected output; a diagnostic lesson
	// need not.
	if !lesson.diagnostic() && !lesson.HasExpected {
		return nil, fmt.Errorf("lesson %s/%s: runnable lesson is missing expected.stdout", section, slug)
	}
	return lesson, nil
}

func (l *Lesson) diagnostic() bool {
	return l.Check != nil && l.Check.Kind == "diagnostic"
}

// CheckResult is the outcome of verifying a run against a lesson's check.
type CheckResult struct {
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// Verify judges a run Response against the lesson's check.
func (l *Lesson) Verify(resp bpipeline.Response) CheckResult {
	if l.diagnostic() {
		return l.verifyDiagnostic(resp)
	}
	return l.verifyStdout(resp)
}

func (l *Lesson) verifyStdout(resp bpipeline.Response) CheckResult {
	if resp.Status != bpipeline.StatusOK {
		return CheckResult{Passed: false, Message: fmt.Sprintf("expected a successful run, got status %q\n%s", resp.Status, failureDetail(resp))}
	}
	out := resp.Program.Stdout
	want := l.Expected
	if l.Check != nil && l.Check.Equals != nil {
		want = *l.Check.Equals
	}
	if l.HasExpected || (l.Check != nil && l.Check.Equals != nil) {
		if out != want {
			return CheckResult{Passed: false, Message: fmt.Sprintf("stdout mismatch:\n got: %q\nwant: %q", out, want)}
		}
	}
	if l.Check != nil {
		for _, sub := range l.Check.Contains {
			if !strings.Contains(out, sub) {
				return CheckResult{Passed: false, Message: fmt.Sprintf("stdout does not contain %q", sub)}
			}
		}
	}
	return CheckResult{Passed: true}
}

func (l *Lesson) verifyDiagnostic(resp bpipeline.Response) CheckResult {
	if resp.Status == bpipeline.StatusOK {
		return CheckResult{Passed: false, Message: "expected compilation to fail, but the program ran successfully"}
	}
	detail := failureDetail(resp)
	for _, sub := range l.Check.Contains {
		if !strings.Contains(detail, sub) {
			return CheckResult{Passed: false, Message: fmt.Sprintf("diagnostic does not contain %q\ngot:\n%s", sub, detail)}
		}
	}
	return CheckResult{Passed: true}
}

// failureDetail gathers the diagnostic text from a non-ok response: the stderr
// of any failing stage plus an internal error message.
func failureDetail(resp bpipeline.Response) string {
	var b strings.Builder
	for _, step := range resp.Steps {
		if step.ExitCode != 0 && step.Stderr != "" {
			b.WriteString(step.Stderr)
			b.WriteByte('\n')
		}
	}
	if resp.Program.Stderr != "" {
		b.WriteString(resp.Program.Stderr)
		b.WriteByte('\n')
	}
	if resp.Error != nil {
		b.WriteString(resp.Error.Message)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// parseFrontMatter splits an optional leading "---" delimited block of
// key: value lines from the markdown body.
func parseFrontMatter(md string) (map[string]string, string, error) {
	front := make(map[string]string)
	rest := md
	rest = strings.TrimPrefix(rest, "\uFEFF") // strip a BOM if present
	if !strings.HasPrefix(rest, "---\n") && !strings.HasPrefix(rest, "---\r\n") {
		return front, md, nil
	}
	nl := strings.IndexByte(rest, '\n')
	rest = rest[nl+1:]
	for {
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return nil, "", fmt.Errorf("front matter: missing closing '---'")
		}
		line := strings.TrimRight(rest[:nl], "\r")
		rest = rest[nl+1:]
		if line == "---" {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", fmt.Errorf("front matter: line %q is not key: value", line)
		}
		front[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	return front, strings.TrimLeft(rest, "\n"), nil
}

// leadingNumber parses the integer prefix of a "NN-name" directory, or 0.
func leadingNumber(name string) int {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, _ := parseInt(name[:i])
	return n
}

func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// humanize turns a "NN-some-name" directory into "Some Name".
func humanize(dir string) string {
	s := dir
	if n := leadingNumber(dir); n != 0 {
		i := 0
		for i < len(dir) && dir[i] >= '0' && dir[i] <= '9' {
			i++
		}
		s = strings.TrimLeft(dir[i:], "-_")
	}
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	fields := strings.Fields(s)
	for i, f := range fields {
		fields[i] = strings.ToUpper(f[:1]) + f[1:]
	}
	return strings.Join(fields, " ")
}
