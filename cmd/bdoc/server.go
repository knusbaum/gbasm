package main

import (
	_ "embed"
	"html/template"
	"net/http"
	"strings"
)

//go:embed styles.css
var stylesCSS []byte

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

// serveCSS serves the embedded stylesheet.
func (d *docState) serveCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(stylesCSS)
}

// serveIndex shows the list of discovered packages.
func (d *docState) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	view := &indexView{Packages: make([]indexEntry, 0, len(d.packages))}
	for _, p := range d.packages {
		view.Packages = append(view.Packages, indexEntry{
			ImportPath: p.ImportPath,
			PkgName:    p.PkgName,
			Blurb:      firstParagraph(p.DocComment),
		})
	}
	if err := indexTmpl.Execute(w, view); err != nil {
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

// -----------------------------------------------------------------------------
// View shapes
// -----------------------------------------------------------------------------

type indexView struct {
	Packages []indexEntry
}

type indexEntry struct {
	ImportPath string
	PkgName    string
	Blurb      string
}

// packageView is the per-package render shape. Sections are populated only
// when non-empty so the template can elide them.
type packageView struct {
	Pkg        *PackageScan
	PkgDisplay string   // PkgName, fallback to ImportPath
	DocParas   []string // overview paragraphs from DocComment

	Vars       []Decl
	FreeFns    []Decl
	Types      []typeGroup
	Interfaces []interfaceGroup
	AsmFuncs   []Decl
	AsmVars    []Decl
	AsmData    []Decl

	HasIndex bool
}

type assocFn struct {
	Anchor    string
	Name      string
	Signature string
	DocParas  []string
	SrcFile   string
	SrcLine   int
}

type typeGroup struct {
	Decl    Decl
	Ctors   []assocFn // free fns returning this type
	Methods []assocFn // from Decl.Methods, anchored as Type.method
}

type interfaceGroup struct {
	Decl    Decl
	Methods []assocFn
}

// -----------------------------------------------------------------------------
// View construction
// -----------------------------------------------------------------------------

func buildPackageView(p *PackageScan) *packageView {
	v := &packageView{Pkg: p}
	v.PkgDisplay = p.PkgName
	if v.PkgDisplay == "" {
		v.PkgDisplay = p.ImportPath
	}
	v.DocParas = splitDoc(p.DocComment)

	var types []Decl
	var freeFns []Decl
	var interfaces []Decl
	for _, d := range p.Decls {
		switch d.Kind {
		case DeclFunc:
			freeFns = append(freeFns, d)
		case DeclType:
			types = append(types, d)
		case DeclInterface:
			interfaces = append(interfaces, d)
		case DeclVar, DeclData:
			v.Vars = append(v.Vars, d)
		case DeclAsmFunc:
			v.AsmFuncs = append(v.AsmFuncs, d)
		case DeclAsmVar:
			v.AsmVars = append(v.AsmVars, d)
		case DeclAsmData:
			v.AsmData = append(v.AsmData, d)
		}
	}

	// A free function is grouped under type T when its return clause references
	// exactly one type from this package. That covers `T`, `owned T`, `*T`,
	// `*owned T`, and anonymous structs like `struct{ fd: T, err: i64 }`. A
	// return that references two or more package types is ambiguous and stays
	// as a free function.
	typeNames := make(map[string]bool, len(types))
	typeIdx := make(map[string]int, len(types))
	for i, t := range types {
		typeNames[t.Name] = true
		typeIdx[t.Name] = i
	}

	groups := make([]typeGroup, len(types))
	for i, t := range types {
		groups[i] = typeGroup{Decl: t}
	}

	claimedFn := make(map[int]bool)
	for i, f := range freeFns {
		assoc := associatedType(f.Signature, typeNames)
		if assoc == "" {
			continue
		}
		gi := typeIdx[assoc]
		groups[gi].Ctors = append(groups[gi].Ctors, assocFn{
			Anchor:    f.Name,
			Name:      f.Name,
			Signature: f.Signature,
			DocParas:  splitDoc(f.Doc),
			SrcFile:   f.SrcFile,
			SrcLine:   f.SrcLine,
		})
		claimedFn[i] = true
	}

	for i, t := range types {
		for _, m := range t.Methods {
			groups[i].Methods = append(groups[i].Methods, assocFn{
				Anchor:    t.Name + "." + m.Name,
				Name:      m.Name,
				Signature: m.Signature,
				DocParas:  splitDoc(m.Doc),
				SrcFile:   m.SrcFile,
				SrcLine:   m.SrcLine,
			})
		}
	}
	v.Types = groups

	for i, f := range freeFns {
		if !claimedFn[i] {
			v.FreeFns = append(v.FreeFns, f)
		}
	}

	for _, iface := range interfaces {
		ig := interfaceGroup{Decl: iface}
		for _, m := range iface.Methods {
			ig.Methods = append(ig.Methods, assocFn{
				Anchor:    iface.Name + "." + m.Name,
				Name:      m.Name,
				Signature: m.Signature,
				DocParas:  splitDoc(m.Doc),
				SrcFile:   m.SrcFile,
				SrcLine:   m.SrcLine,
			})
		}
		v.Interfaces = append(v.Interfaces, ig)
	}

	v.HasIndex = len(v.Vars) > 0 || len(v.FreeFns) > 0 || len(v.Types) > 0 ||
		len(v.Interfaces) > 0 || len(v.AsmFuncs) > 0 || len(v.AsmVars) > 0 || len(v.AsmData) > 0

	return v
}

// firstParagraph returns the first paragraph of a doc comment (used for the
// index-page blurb).
func firstParagraph(doc string) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	if i := strings.Index(doc, "\n\n"); i >= 0 {
		doc = doc[:i]
	}
	return strings.ReplaceAll(doc, "\n", " ")
}

// splitDoc breaks a doc comment into paragraphs separated by blank lines.
func splitDoc(s string) []string {
	var out []string
	for _, para := range strings.Split(s, "\n\n") {
		para = strings.TrimSpace(para)
		if para != "" {
			out = append(out, strings.ReplaceAll(para, "\n", " "))
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// Templates
// -----------------------------------------------------------------------------

var tmplFuncs = template.FuncMap{
	"highlight":  highlight,
	"paragraphs": splitDoc,
	"hasStructBody": func(d Decl) bool {
		return strings.HasPrefix(strings.TrimSpace(d.Body), "struct")
	},
}

const chromeBlock = `
{{define "chrome"}}
<header class="chrome">
  <div class="wordmark">
    <span class="dot"></span>
    <a href="/"><span>bdoc</span></a>
  </div>
  <div class="search">
    <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
      <circle cx="7" cy="7" r="4.5" stroke="currentColor" stroke-width="1.4"/>
      <path d="M10.5 10.5L14 14" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
    </svg>
    <span>Search packages, types, functions…</span>
    <span class="kbd">/</span>
  </div>
  <nav class="nav">
    <a href="/" class="current">Packages</a>
  </nav>
</header>
{{end}}
`

var indexTmpl = template.Must(template.New("index").Funcs(tmplFuncs).Parse(chromeBlock + `
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Bdoc — Boson packages</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Sans:wght@400;500;600;700&family=IBM+Plex+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/styles.css">
</head>
<body>
<div class="bdoc">
{{template "chrome" .}}
<main class="main solo">
  <section class="pkg-head">
    <span class="kind">index</span>
    <h1><span class="name">packages</span><span class="label">— discovered Boson packages</span></h1>
  </section>
  {{if .Packages}}
  <ul class="pkglist">
    {{range .Packages}}
    <li>
      <div class="row">
        <a href="/pkg/{{.ImportPath}}/" class="ipath">{{.ImportPath}}</a>
        {{if .PkgName}}<span class="pkgname">package {{.PkgName}}</span>{{end}}
      </div>
      {{if .Blurb}}<div class="blurb">{{.Blurb}}</div>{{end}}
    </li>
    {{end}}
  </ul>
  {{else}}
  <p class="empty">No packages found. Set BOSONPATH or pass -path.</p>
  {{end}}
</main>
</div>
</body>
</html>
`))

// pkgTmpl renders the per-package documentation page.
var pkgTmpl = template.Must(template.New("pkg").Funcs(tmplFuncs).Parse(chromeBlock + `
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Bdoc — package {{.PkgDisplay}}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Sans:wght@400;500;600;700&family=IBM+Plex+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/styles.css">
</head>
<body>
<div class="bdoc">
{{template "chrome" .}}
<div class="layout">
  <aside class="toc">
    <div class="toc-label">in this package</div>
    <ul>
      <li><a href="#overview" class="toc-section"><span>Overview</span></a></li>
      {{if .HasIndex}}<li><a href="#index" class="toc-section"><span>Index</span></a></li>{{end}}
      {{if .Vars}}
      <li>
        <a href="#variables" class="toc-section"><span>Variables</span><span class="count">{{len .Vars}}</span></a>
        <ul class="sub">{{range .Vars}}<li><a href="#{{.Name}}">{{.Name}}</a></li>{{end}}</ul>
      </li>
      {{end}}
      {{if .FreeFns}}
      <li>
        <a href="#functions" class="toc-section"><span>Functions</span><span class="count">{{len .FreeFns}}</span></a>
        <ul class="sub">{{range .FreeFns}}<li><a href="#{{.Name}}">{{.Name}}()</a></li>{{end}}</ul>
      </li>
      {{end}}
      {{if .Types}}
      <li>
        <a href="#types" class="toc-section"><span>Types</span><span class="count">{{len .Types}}</span></a>
        <ul class="sub">{{range .Types}}<li><a href="#{{.Decl.Name}}">{{.Decl.Name}}</a></li>{{end}}</ul>
      </li>
      {{end}}
      {{if .Interfaces}}
      <li>
        <a href="#interfaces" class="toc-section"><span>Interfaces</span><span class="count">{{len .Interfaces}}</span></a>
        <ul class="sub">{{range .Interfaces}}<li><a href="#{{.Decl.Name}}">{{.Decl.Name}}</a></li>{{end}}</ul>
      </li>
      {{end}}
      {{if .AsmFuncs}}
      <li>
        <a href="#asm-functions" class="toc-section"><span>Asm functions</span><span class="count">{{len .AsmFuncs}}</span></a>
        <ul class="sub">{{range .AsmFuncs}}<li><a href="#{{.Name}}">{{.Name}}</a></li>{{end}}</ul>
      </li>
      {{end}}
      {{if .AsmVars}}
      <li>
        <a href="#asm-variables" class="toc-section"><span>Asm variables</span><span class="count">{{len .AsmVars}}</span></a>
        <ul class="sub">{{range .AsmVars}}<li><a href="#{{.Name}}">{{.Name}}</a></li>{{end}}</ul>
      </li>
      {{end}}
      {{if .AsmData}}
      <li>
        <a href="#asm-data" class="toc-section"><span>Asm data</span><span class="count">{{len .AsmData}}</span></a>
        <ul class="sub">{{range .AsmData}}<li><a href="#{{.Name}}">{{.Name}}</a></li>{{end}}</ul>
      </li>
      {{end}}
    </ul>
    {{if .Pkg.SrcFiles}}
    <div class="toc-foot">
      Defined in
      <ul class="src-list">{{range .Pkg.SrcFiles}}<li>{{.}}</li>{{end}}</ul>
    </div>
    {{end}}
  </aside>

  <main class="main">
    <div class="crumbs">
      <a href="/">packages</a>
      <span class="sep">/</span>
      <span class="current">{{.PkgDisplay}}</span>
    </div>

    <section class="pkg-head" id="overview">
      <span class="kind">package</span>
      <h1><span class="name">{{.PkgDisplay}}</span></h1>
      <div class="import">
        <span><span class="k">import</span> <span class="str">"{{.Pkg.ImportPath}}"</span></span>
      </div>
      {{if .DocParas}}
      <div class="overview">
        {{range .DocParas}}<p>{{.}}</p>{{end}}
      </div>
      {{end}}
    </section>

    {{if .HasIndex}}
    <section class="index" id="index">
      <div class="index-head"><h2>Index</h2></div>

      {{if .Vars}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#variables">Variables</a></h3>
        <ul>{{range .Vars}}<li><a href="#{{.Name}}">{{highlight .Signature}}</a></li>{{end}}</ul>
      </div>
      {{end}}

      {{if .FreeFns}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#functions">Functions</a></h3>
        <ul>{{range .FreeFns}}<li><a href="#{{.Name}}">{{highlight .Signature}}</a></li>{{end}}</ul>
      </div>
      {{end}}

      {{if .Types}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#types">Types</a></h3>
        <ul>{{range .Types}}
          <li><a href="#{{.Decl.Name}}">{{highlight .Decl.Signature}}</a></li>
          {{range .Ctors}}<li class="sub"><a href="#{{.Anchor}}">{{highlight .Signature}}</a></li>{{end}}
          {{range .Methods}}<li class="sub"><a href="#{{.Anchor}}">{{highlight .Signature}}</a></li>{{end}}
        {{end}}</ul>
      </div>
      {{end}}

      {{if .Interfaces}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#interfaces">Interfaces</a></h3>
        <ul>{{range .Interfaces}}
          <li><a href="#{{.Decl.Name}}">{{highlight .Decl.Signature}}</a></li>
          {{range .Methods}}<li class="sub"><a href="#{{.Anchor}}">{{highlight .Signature}}</a></li>{{end}}
        {{end}}</ul>
      </div>
      {{end}}

      {{if .AsmFuncs}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#asm-functions">Assembly functions</a></h3>
        <ul>{{range .AsmFuncs}}<li><a href="#{{.Name}}">{{highlight .Signature}}</a></li>{{end}}</ul>
      </div>
      {{end}}

      {{if .AsmVars}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#asm-variables">Assembly variables</a></h3>
        <ul>{{range .AsmVars}}<li><a href="#{{.Name}}">{{highlight .Signature}}</a></li>{{end}}</ul>
      </div>
      {{end}}

      {{if .AsmData}}
      <div class="index-group">
        <h3 class="index-group-label"><a href="#asm-data">Assembly data</a></h3>
        <ul>{{range .AsmData}}<li><a href="#{{.Name}}">{{highlight .Signature}}</a></li>{{end}}</ul>
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .Vars}}
    <section class="section" id="variables">
      <div class="section-head">
        <h2>Variables <span class="count">{{len .Vars}}</span></h2>
      </div>
      {{range .Vars}}
      <div class="decl" id="{{.Name}}">
        <div class="sig var-sig">{{highlight .Signature}}</div>
        {{template "doc" .Doc}}
        {{template "meta" .}}
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .FreeFns}}
    <section class="section" id="functions">
      <div class="section-head">
        <h2>Functions <span class="count">{{len .FreeFns}}</span></h2>
      </div>
      {{range .FreeFns}}
      <div class="decl" id="{{.Name}}">
        <div class="sig fn-sig">{{highlight .Signature}}</div>
        {{template "doc" .Doc}}
        {{template "meta" .}}
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .Types}}
    <section class="section" id="types">
      <div class="section-head">
        <h2>Types <span class="count">{{len .Types}}</span></h2>
      </div>
      {{range .Types}}
      <div class="decl decl-type" id="{{.Decl.Name}}">
        <div class="sig type-sig">{{highlight .Decl.Signature}}</div>
        {{if hasStructBody .Decl}}<pre class="struct-body">{{highlight .Decl.Body}}</pre>{{end}}
        {{template "doc" .Decl.Doc}}
        <div class="meta">
          <span>{{.Decl.SrcFile}}:{{.Decl.SrcLine}}</span>
          <a href="#{{.Decl.Name}}" class="anchor">#{{.Decl.Name}}</a>
        </div>

        {{if .Ctors}}
        <div class="assoc">
          <div class="assoc-label">Functions</div>
          {{range .Ctors}}{{template "assocFn" .}}{{end}}
        </div>
        {{end}}

        {{if .Methods}}
        <div class="assoc">
          <div class="assoc-label">Methods</div>
          {{range .Methods}}{{template "assocMethod" .}}{{end}}
        </div>
        {{end}}
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .Interfaces}}
    <section class="section" id="interfaces">
      <div class="section-head">
        <h2>Interfaces <span class="count">{{len .Interfaces}}</span></h2>
      </div>
      {{range .Interfaces}}
      <div class="decl decl-type" id="{{.Decl.Name}}">
        <div class="sig interface-sig">{{highlight .Decl.Signature}}</div>
        {{template "doc" .Decl.Doc}}
        <div class="meta">
          <span>{{.Decl.SrcFile}}:{{.Decl.SrcLine}}</span>
          <a href="#{{.Decl.Name}}" class="anchor">#{{.Decl.Name}}</a>
        </div>
        {{if .Methods}}
        <div class="assoc">
          <div class="assoc-label">Methods</div>
          {{range .Methods}}{{template "assocMethod" .}}{{end}}
        </div>
        {{end}}
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .AsmFuncs}}
    <section class="section" id="asm-functions">
      <div class="section-head">
        <h2>Assembly functions <span class="count">{{len .AsmFuncs}}</span></h2>
      </div>
      {{range .AsmFuncs}}
      <div class="decl" id="{{.Name}}">
        <div class="sig fn-sig">{{highlight .Signature}}</div>
        {{template "doc" .Doc}}
        {{template "meta" .}}
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .AsmVars}}
    <section class="section" id="asm-variables">
      <div class="section-head">
        <h2>Assembly variables <span class="count">{{len .AsmVars}}</span></h2>
      </div>
      {{range .AsmVars}}
      <div class="decl" id="{{.Name}}">
        <div class="sig var-sig">{{highlight .Signature}}</div>
        {{template "doc" .Doc}}
        {{template "meta" .}}
      </div>
      {{end}}
    </section>
    {{end}}

    {{if .AsmData}}
    <section class="section" id="asm-data">
      <div class="section-head">
        <h2>Assembly data <span class="count">{{len .AsmData}}</span></h2>
      </div>
      {{range .AsmData}}
      <div class="decl" id="{{.Name}}">
        <div class="sig data-sig">{{highlight .Signature}}</div>
        {{template "doc" .Doc}}
        {{template "meta" .}}
      </div>
      {{end}}
    </section>
    {{end}}

  </main>
</div>
</div>
</body>
</html>

{{define "doc"}}{{if .}}<div class="doc">{{range paragraphs .}}<p>{{.}}</p>{{end}}</div>{{end}}{{end}}

{{define "meta"}}<div class="meta">
  <span>{{.SrcFile}}:{{.SrcLine}}</span>
  <a href="#{{.Name}}" class="anchor">#{{.Name}}</a>
</div>{{end}}

{{define "assocFn"}}<div class="decl" id="{{.Anchor}}">
  <div class="sig fn-sig">{{highlight .Signature}}</div>
  {{if .DocParas}}<div class="doc">{{range .DocParas}}<p>{{.}}</p>{{end}}</div>{{end}}
  <div class="meta">
    <span>{{.SrcFile}}:{{.SrcLine}}</span>
    <a href="#{{.Anchor}}" class="anchor">#{{.Anchor}}</a>
  </div>
</div>{{end}}

{{define "assocMethod"}}<div class="decl" id="{{.Anchor}}">
  <div class="sig method-sig">{{highlight .Signature}}</div>
  {{if .DocParas}}<div class="doc">{{range .DocParas}}<p>{{.}}</p>{{end}}</div>{{end}}
  <div class="meta">
    {{if .SrcFile}}<span>{{.SrcFile}}:{{.SrcLine}}</span>{{end}}
    <a href="#{{.Anchor}}" class="anchor">#{{.Anchor}}</a>
  </div>
</div>{{end}}
`))

