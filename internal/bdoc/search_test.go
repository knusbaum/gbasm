package bdoc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSearchViewFindsPackagesDeclsAndMethods(t *testing.T) {
	packages := []*PackageScan{
		{
			ImportPath: "errors",
			PkgName:    "errors",
			DocComment: "Error package docs.",
			Decls: []Decl{
				{
					Kind:      DeclType,
					Name:      "io_error",
					Signature: "type io_error values { ... }",
					Doc:       "I/O error values.",
					IsPub:     true,
					SrcFile:   "errors.bos",
					SrcLine:   3,
					Methods: []Decl{
						{
							Kind:      DeclFunc,
							Name:      "message",
							Signature: "message() byte[]",
							Doc:       "Human-readable error text.",
							IsPub:     true,
							SrcFile:   "errors.bos",
							SrcLine:   12,
						},
					},
				},
			},
		},
	}

	got := buildSearchView(packages, "message", "")
	if len(got.Symbols) != 1 {
		t.Fatalf("expected one symbol result, got packages=%d symbols=%d", len(got.Packages), len(got.Symbols))
	}
	if got.Symbols[0].Kind != "method" {
		t.Fatalf("expected method result, got %+v", got.Symbols[0])
	}
	if got.Symbols[0].URL != "/pkg/errors/#io_error.message" {
		t.Fatalf("unexpected method URL: %q", got.Symbols[0].URL)
	}

	got = buildSearchView(packages, "ERRORS", "")
	if len(got.Packages) != 1 {
		t.Fatalf("expected case-insensitive package result, got %+v", got)
	}
	if got.Packages[0].URL != "/pkg/errors/" {
		t.Fatalf("unexpected package URL: %q", got.Packages[0].URL)
	}
}

func TestBuildSearchViewRanksExactBeforeSubstring(t *testing.T) {
	packages := []*PackageScan{
		{
			ImportPath: "alpha",
			PkgName:    "alpha",
			Decls: []Decl{
				{Kind: DeclFunc, Name: "find", Signature: "pub fn find() i64", IsPub: true},
				{Kind: DeclFunc, Name: "refind", Signature: "pub fn refind() i64", IsPub: true},
			},
		},
	}

	got := buildSearchView(packages, "find", "")
	if len(got.Symbols) != 2 {
		t.Fatalf("expected two symbols, got %+v", got.Symbols)
	}
	if got.Symbols[0].Name != "find" {
		t.Fatalf("expected exact match first, got %+v", got.Symbols)
	}
}

func TestServeSearch(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "io")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package io

// Opens a file descriptor.
pub fn open(path byte[]) i64 {
	return 0
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "io.bos"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	state := newDocState(root, "")
	req := httptest.NewRequest(http.MethodGet, "/search?q=open", nil)
	rec := httptest.NewRecorder()
	state.serveSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`value="open"`, `/pkg/io/#open`, `Opens a file descriptor.`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q:\n%s", want, body)
		}
	}
}

func TestHandlerServesUnderBasePath(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "io")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package io

// Opens a file descriptor.
pub fn open(path byte[]) i64 {
	return 0
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "io.bos"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := Handler(Options{BosonPath: root, BasePath: "/docs"})
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/docs/", want: `/docs/pkg/io/`},
		{path: "/docs/pkg/io/", want: `href="/docs/"`},
		{path: "/docs/search?q=open", want: `/docs/pkg/io/#open`},
		{path: "/docs/styles.css", want: `.bdoc`},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body: %s", tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s response missing %q:\n%s", tc.path, tc.want, rec.Body.String())
		}
	}
}
