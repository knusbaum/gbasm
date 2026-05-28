package main

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

// Mirrors the JSX highlighter in variants.jsx. Tokenises a signature line
// into identifiers / numbers / whitespace / strings / single punctuation
// characters and wraps each token in a span class:
//
//   .k   keyword
//   .t   primitive or capitalised type name
//   .id  ordinary identifier
//   .num numeric literal
//   .c   ALL_CAPS constant
//   .p   punctuation
//   .str string literal
//
// Anything that doesn't match is passed through as plain (escaped) text.

var bosonKeywords = map[string]bool{
	"fn": true, "var": true, "type": true, "mut": true, "owned": true,
	"package": true, "import": true, "const": true, "struct": true,
	"enum": true, "if": true, "else": true, "return": true,
	"true": true, "false": true, "nil": true, "for": true, "while": true,
	"interface": true, "function": true, "data": true,
}

var bosonPrimitives = map[string]bool{
	"i64": true, "i32": true, "i16": true, "i8": true,
	"u64": true, "u32": true, "u16": true, "u8": true,
	"f64": true, "f32": true,
	"byte": true, "bool": true, "str": true, "void": true,
}

var (
	tokenRe   = regexp.MustCompile(`[A-Za-z_]\w*|\d+|\s+|"[^"]*"|[^\w\s]`)
	identRe   = regexp.MustCompile(`^[A-Za-z_]\w*$`)
	digitsRe  = regexp.MustCompile(`^\d+$`)
	constRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)
	typeRe    = regexp.MustCompile(`^[A-Z]`)
	lowerRe   = regexp.MustCompile(`^[a-z_]\w*$`)
	stringRe  = regexp.MustCompile(`^"[^"]*"$`)
	wsRe      = regexp.MustCompile(`^\s+$`)
	punctSet  = `()[]{}<>,;:.*&=`
)

// highlight returns the signature as syntax-highlighted HTML.
func highlight(sig string) template.HTML {
	tokens := tokenRe.FindAllString(sig, -1)
	var b strings.Builder
	for _, t := range tokens {
		switch {
		case wsRe.MatchString(t):
			b.WriteString(t) // preserve whitespace verbatim
		case stringRe.MatchString(t):
			writeSpan(&b, "str", t)
		case bosonKeywords[t]:
			writeSpan(&b, "k", t)
		case bosonPrimitives[t]:
			writeSpan(&b, "t", t)
		case digitsRe.MatchString(t):
			writeSpan(&b, "num", t)
		case constRe.MatchString(t):
			writeSpan(&b, "c", t)
		case typeRe.MatchString(t):
			writeSpan(&b, "t", t)
		case lowerRe.MatchString(t):
			writeSpan(&b, "id", t)
		case len(t) == 1 && strings.ContainsRune(punctSet, rune(t[0])):
			writeSpan(&b, "p", t)
		default:
			b.WriteString(html.EscapeString(t))
		}
	}
	return template.HTML(b.String())
}

func writeSpan(b *strings.Builder, class, text string) {
	b.WriteString(`<span class="`)
	b.WriteString(class)
	b.WriteString(`">`)
	b.WriteString(html.EscapeString(text))
	b.WriteString(`</span>`)
}

// returnClause returns the portion of a function signature after the
// parameter list — the return type expression. Paren depth is tracked so
// nested parens inside params do not confuse the cut.
func returnClause(sig string) string {
	depth := 0
	opened := false
	for i, c := range sig {
		switch c {
		case '(':
			depth++
			opened = true
		case ')':
			depth--
			if opened && depth == 0 {
				return strings.TrimSpace(sig[i+1:])
			}
		}
	}
	return ""
}

var identAllRe = regexp.MustCompile(`[A-Za-z_]\w*`)

// returnIgnore are tokens to skip when scanning a return clause for package
// type references. They are syntactic decoration, not types.
var returnIgnore = map[string]bool{
	"owned": true, "mut": true, "struct": true, "enum": true, "nullable": true,
}

// associatedType reports which package-local type, if any, a free function's
// return clause is "about". A function is associated with type T when its
// return clause references exactly one distinct type from packageTypes.
// Multiple matches → ambiguous, no association (treated as a plain free fn).
func associatedType(sig string, packageTypes map[string]bool) string {
	rc := returnClause(sig)
	if rc == "" {
		return ""
	}
	seen := make(map[string]struct{})
	for _, tok := range identAllRe.FindAllString(rc, -1) {
		if returnIgnore[tok] || bosonKeywords[tok] || bosonPrimitives[tok] {
			continue
		}
		if packageTypes[tok] {
			seen[tok] = struct{}{}
		}
	}
	if len(seen) != 1 {
		return ""
	}
	for k := range seen {
		return k
	}
	return ""
}
