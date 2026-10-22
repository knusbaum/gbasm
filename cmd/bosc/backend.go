package main

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/knusbaum/gbasm"
)

// assemblyBackend owns the assembly dialect emitted after semantic lowering.
// The initial ARM backend adapts the existing two-operand instruction stream;
// named storage and metadata retain the shared Boson metaassembly syntax.
type assemblyBackend interface {
	Emit(io.Writer, []byte) error
}
type amd64Backend struct{}

func (amd64Backend) Emit(w io.Writer, text []byte) error { _, e := w.Write(text); return e }

type arm64Backend struct{}

var registerToken = regexp.MustCompile(`\b(?:r(?:ax|bx|cx|dx|si|di|sp|bp|[89]|1[0-5])(?:d|w|b)?|e(?:ax|bx|cx|dx|si|di|sp|bp)|[abcd][lh]|[sd]il|[sb]pl|[abcd]x|[sd]i|[sb]p|xmm[0-9]+)\b`)

func armRegister(r string) string {
	if strings.HasPrefix(r, "xmm") {
		return "d" + strings.TrimPrefix(r, "xmm")
	}
	full := map[string]string{"rax": "x8", "rbx": "x19", "rcx": "x3", "rdx": "x2", "rsi": "x1", "rdi": "x0", "rsp": "sp", "rbp": "x29", "r8": "x4", "r9": "x5", "r10": "x9", "r11": "x10", "r12": "x20", "r13": "x21", "r14": "x22", "r15": "x23"}
	if x := full[r]; x != "" {
		return x
	}
	aliases := map[string]string{"eax": "rax", "ax": "rax", "al": "rax", "ebx": "rbx", "bx": "rbx", "bl": "rbx", "ecx": "rcx", "cx": "rcx", "cl": "rcx", "edx": "rdx", "dx": "rdx", "dl": "rdx", "esi": "rsi", "si": "rsi", "sil": "rsi", "edi": "rdi", "di": "rdi", "dil": "rdi"}
	if base := aliases[r]; base != "" {
		return "w" + strings.TrimPrefix(full[base], "x")
	}
	if len(r) > 2 {
		last := r[len(r)-1]
		if last == 'd' || last == 'w' || last == 'b' {
			if x := full[r[:len(r)-1]]; x != "" {
				return "w" + strings.TrimPrefix(x, "x")
			}
		}
	}
	return r
}

var assemblyIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*`)

func (arm64Backend) Emit(w io.Writer, text []byte) error {
	var result bytes.Buffer
	lines := strings.Split(string(text), "\n")
	globals := map[string]bool{}
	pkg := ""
	for _, line := range lines {
		p := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "pub ")))
		if len(p) > 1 {
			if p[0] == "package" {
				pkg = p[1]
			}
			if p[0] == "var" || p[0] == "data" {
				globals[p[1]] = true
			}
		}
	}
	locals := map[string]string{}
	inFunction := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		p := strings.Fields(trimmed)
		if len(p) > 0 && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(trimmed, "//") {
			inFunction = false
		}
		if len(p) > 1 && (p[0] == "function" || (p[0] == "pub" && p[1] == "function")) {
			locals = map[string]string{}
			inFunction = true
		}
		if inFunction && len(p) > 1 && (p[0] == "local" || p[0] == "bytes" || p[0] == "argi" || p[0] == "arg") {
			locals[p[1]] = "__arm_" + p[1]
		}
		translate := func(token string) string {
			if name := locals[token]; name != "" {
				return name
			}
			if globals[token] {
				return pkg + "." + token
			}
			if registerToken.FindString(token) == token {
				return armRegister(token)
			}
			return token
		}
		if inFunction && len(p) > 0 && strings.HasPrefix(line, "\t") && !strings.HasPrefix(trimmed, "//") && p[0] != "type" && p[0] != "retaliases" {
			op := p[0]
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, op))
			switch op {
			case "local", "bytes", "argi", "arg":
				args := append([]string(nil), p[1:]...)
				args[0] = locals[args[0]]
				if (op == "local" || op == "bytes") && len(args) == 3 {
					args[2] = armRegister(args[2])
				}
				if op == "arg" && len(args) > 1 {
					args[1] = armRegister(args[1])
				}
				rest = strings.Join(args, " ")
			case "inreg":
				rest = translate(p[1]) + " " + armRegister(p[2])
			case "acquire", "release", "use":
				rest = registerToken.ReplaceAllStringFunc(rest, armRegister)
			default:
				rest = assemblyIdentifier.ReplaceAllStringFunc(rest, translate)
			}
			if op == "movzx" || op == "movsx" {
				original := p[len(p)-1]
				bits := 0
				// Binding widths are carried by their declarations, whereas raw x86
				// byte and halfword views need an explicit extension width.
				if locals[original] == "" {
					switch original {
					case "al", "bl", "cl", "dl", "sil", "dil":
						bits = 8
					case "ax", "bx", "cx", "dx", "si", "di":
						bits = 16
					}
				}
				if bits != 0 {
					op += strconv.Itoa(bits)
				}
			}
			if (op == "shl" || op == "shr" || op == "sar") && len(strings.Fields(rest)) == 1 {
				rest += " x3"
			}
			if op == "ret" {
				fmt.Fprintln(&result, "\tmov x0 x8")
			}
			line = "\t" + op + " " + rest
		}
		fmt.Fprintln(&result, line)
		if strings.HasPrefix(line, "package ") {
			fmt.Fprintln(&result, "target darwin/arm64")
		}
	}
	_, err := w.Write(result.Bytes())
	return err
}

func backendFor(t gbasm.Target) assemblyBackend {
	if t == gbasm.DarwinARM64 {
		return arm64Backend{}
	}
	return amd64Backend{}
}
