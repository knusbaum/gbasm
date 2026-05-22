package main

import (
	"html/template"
	"net/http"
	"sort"
	"strings"
)

// docState holds the discovered package set for serving.
type docState struct {
	packages []*PackageScan
	byPath   map[string]*PackageScan
}

func newDocState(packages []*PackageScan) *docState {
	d := &docState{packages: packages, byPath: make(map[string]*PackageScan)}
	for _, p := range packages {
		d.byPath[p.ImportPath] = p
	}
	return d
}

// serveIndex shows the list of discovered packages.
func (d *docState) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if err := indexTmpl.Execute(w, d.packages); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// servePkg shows one package's contents.
func (d *docState) servePkg(w http.ResponseWriter, r *http.Request) {
	importPath := strings.TrimPrefix(r.URL.Path, "/pkg/")
	importPath = strings.TrimSuffix(importPath, "/")
	pkg, ok := d.byPath[importPath]
	if !ok {
		http.NotFound(w, r)
		return
	}

	view := buildPackageView(pkg)
	if err := pkgTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// packageView is the template-friendly shape for a package page.
type packageView struct {
	Pkg        *PackageScan
	Funcs      []Decl
	Structs    []Decl
	TypeDecls  []Decl
	VarDecls   []Decl
	DataDecls  []Decl
	AsmFuncs   []Decl
	AsmVars    []Decl
	AsmData    []Decl
	DocLines   []string // doc comment split into paragraphs
}

func buildPackageView(p *PackageScan) *packageView {
	v := &packageView{Pkg: p}
	for _, d := range p.Decls {
		switch d.Kind {
		case DeclFunc:
			v.Funcs = append(v.Funcs, d)
		case DeclStruct:
			v.Structs = append(v.Structs, d)
		case DeclTypeAlias:
			v.TypeDecls = append(v.TypeDecls, d)
		case DeclVar:
			v.VarDecls = append(v.VarDecls, d)
		case DeclData:
			v.DataDecls = append(v.DataDecls, d)
		case DeclAsmFunc:
			v.AsmFuncs = append(v.AsmFuncs, d)
		case DeclAsmVar:
			v.AsmVars = append(v.AsmVars, d)
		case DeclAsmData:
			v.AsmData = append(v.AsmData, d)
		}
	}
	for _, list := range [][]Decl{v.Funcs, v.Structs, v.TypeDecls, v.VarDecls, v.DataDecls, v.AsmFuncs, v.AsmVars, v.AsmData} {
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	}
	if p.DocComment != "" {
		v.DocLines = splitDoc(p.DocComment)
	}
	return v
}

// splitDoc breaks a doc comment into paragraphs separated by blank lines.
func splitDoc(s string) []string {
	var out []string
	for _, para := range strings.Split(s, "\n\n") {
		para = strings.TrimSpace(para)
		if para != "" {
			out = append(out, para)
		}
	}
	return out
}

// ---- HTML templates ---------------------------------------------------------

var tmplFuncs = template.FuncMap{
	"isAsm": func(d Decl) bool {
		return d.Kind == DeclAsmFunc || d.Kind == DeclAsmVar || d.Kind == DeclAsmData
	},
}

const baseCSS = `
body { font-family: -apple-system, sans-serif; max-width: 900px; margin: 2em auto; padding: 0 1em; color: #222; }
h1, h2, h3 { color: #003a70; }
a { color: #003a70; }
a:hover { text-decoration: underline; }
code, pre { font-family: "SF Mono", Menlo, Consolas, monospace; }
pre { background: #f5f5f5; padding: 0.8em 1em; border-radius: 4px; overflow-x: auto; font-size: 0.9em; }
.decl { margin: 1.5em 0; }
.decl .sig { font-family: "SF Mono", Menlo, Consolas, monospace; font-weight: 600; color: #111; background: #f5f5f5; padding: 0.5em 0.8em; border-left: 3px solid #003a70; }
.decl .doc { margin: 0.5em 0 0 0; }
.decl .doc p { margin: 0.5em 0; }
.srcref { color: #888; font-size: 0.85em; }
.pkglist li { margin: 0.3em 0; }
.pkglist code { background: none; }
.pkgname { font-size: 0.9em; color: #888; }
.kind-tag { display: inline-block; padding: 0.1em 0.5em; background: #eee; color: #555; font-size: 0.75em; border-radius: 3px; margin-right: 0.5em; }
hr { border: none; border-top: 1px solid #ddd; margin: 2em 0; }
`

var indexFuncs = template.FuncMap{
	"firstLine": func(s string) string {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[:i]
		}
		return s
	},
}

var indexTmpl = template.Must(template.New("index").Funcs(indexFuncs).Parse(`
<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<title>Boson packages</title>
<style>` + baseCSS + `</style>
</head><body>
<h1>Boson packages</h1>
<ul class="pkglist">
{{range .}}
  <li>
    <a href="/pkg/{{.ImportPath}}/"><code>{{.ImportPath}}</code></a>
    {{if .PkgName}}<span class="pkgname">(package {{.PkgName}})</span>{{end}}
    {{if .DocComment}}<br><span class="srcref">{{firstLine .DocComment}}</span>{{end}}
  </li>
{{else}}
  <li>No packages found. Set BOSONPATH or pass -path.</li>
{{end}}
</ul>
</body></html>
`))

var pkgTmpl = template.Must(template.New("pkg").Funcs(tmplFuncs).Parse(`
<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<title>{{.Pkg.ImportPath}} - Boson docs</title>
<style>` + baseCSS + `</style>
</head><body>
<p><a href="/">← all packages</a></p>
<h1>package <code>{{if .Pkg.PkgName}}{{.Pkg.PkgName}}{{else}}{{.Pkg.ImportPath}}{{end}}</code></h1>
<p class="srcref">import path: <code>{{.Pkg.ImportPath}}</code></p>

{{if .DocLines}}
<div class="doc">
  {{range .DocLines}}<p>{{.}}</p>{{end}}
</div>
{{end}}

{{if .TypeDecls}}
<h2>Types</h2>
{{range .TypeDecls}}
<div class="decl">
  <div class="sig">{{.Signature}}</div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

{{if .Structs}}
<h2>Structs</h2>
{{range .Structs}}
<div class="decl">
  <div class="sig"><pre>{{.Body}}</pre></div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

{{if .Funcs}}
<h2>Functions</h2>
{{range .Funcs}}
<div class="decl">
  <div class="sig">{{.Signature}}</div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

{{if .VarDecls}}
<h2>Variables</h2>
{{range .VarDecls}}
<div class="decl">
  <div class="sig">{{.Signature}}</div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

{{if .AsmFuncs}}
<h2>Functions (assembly)</h2>
{{range .AsmFuncs}}
<div class="decl">
  <div class="sig">{{.Signature}}</div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

{{if .AsmVars}}
<h2>Variables (assembly)</h2>
{{range .AsmVars}}
<div class="decl">
  <div class="sig">{{.Signature}}</div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

{{if .AsmData}}
<h2>Data (assembly)</h2>
{{range .AsmData}}
<div class="decl">
  <div class="sig">{{.Signature}}</div>
  {{if .Doc}}<div class="doc"><p>{{.Doc}}</p></div>{{end}}
  <p class="srcref">{{.SrcFile}}:{{.SrcLine}}</p>
</div>
{{end}}
{{end}}

<hr>
<h3>Source files</h3>
<ul>
{{range .Pkg.SrcFiles}}<li><code>{{.}}</code></li>{{end}}
</ul>
</body></html>
`))

