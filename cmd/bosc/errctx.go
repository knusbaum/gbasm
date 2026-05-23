package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

const (
	ansiReset = "\033[0m"
	ansiRed   = "\033[31m"
)

// stderrIsTTY reports whether os.Stderr looks like an interactive terminal,
// so we can suppress ANSI color escapes when output is being captured.
func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// printErrorContext writes a short source snippet centered on err's reported
// position, with an arrow under the offending column. Best-effort: any I/O
// or type-assertion failure causes it to silently skip the snippet, since
// the caller has already printed the primary diagnostic.
func printErrorContext(w io.Writer, err error) {
	ie, ok := err.(*interpreterError)
	if !ok || ie.p.fname == "" || ie.p.lineoff == 0 {
		return
	}
	f, ferr := os.Open(ie.p.fname)
	if ferr != nil {
		return
	}
	defer f.Close()

	const ctx = 2 // lines of context above and below the error line
	errLineNo := int(ie.p.lineoff)
	startLine := errLineNo - ctx
	if startLine < 1 {
		startLine = 1
	}
	endLine := errLineNo + ctx

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	line := 0
	for scanner.Scan() {
		line++
		if line < startLine {
			continue
		}
		if line > endLine {
			break
		}
		text := scanner.Text()
		fmt.Fprintln(w, text)
		if line != errLineNo {
			continue
		}
		// Build the underline: copy whitespace prefix runes verbatim so
		// tabs continue to align; for everything else emit a dash up to
		// the error column, then a caret.
		col := int(ie.p.linecharoff) - 1
		if col < 0 {
			col = 0
		}
		var arrow strings.Builder
		i := 0
		for _, r := range text {
			if i >= col {
				break
			}
			if r == '\t' {
				arrow.WriteRune('\t')
			} else {
				arrow.WriteRune('-')
			}
			i++
		}
		// If the column lies past end-of-line (common when the lexer
		// reports the position just after consuming the bad token),
		// pad with dashes so the caret still appears at that column.
		for i < col {
			arrow.WriteRune('-')
			i++
		}
		if stderrIsTTY() {
			fmt.Fprintf(w, "%s%s^%s\n", ansiRed, arrow.String(), ansiReset)
		} else {
			fmt.Fprintf(w, "%s^\n", arrow.String())
		}
	}
}

// fatalCtx logs the formatted message via log.Printf, prints a source-context
// snippet for any *interpreterError found among args, and exits with status 1.
// Use this in place of log.Fatalf when one of the format arguments is an
// error that may carry positional information.
func fatalCtx(format string, args ...interface{}) {
	log.Printf(format, args...)
	for _, a := range args {
		if e, ok := a.(error); ok {
			printErrorContext(os.Stderr, e)
		}
	}
	os.Exit(1)
}
