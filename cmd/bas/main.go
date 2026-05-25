package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/bits"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/knusbaum/gbasm"
)

var jumps = []string{
	"JA",
	"JAE",
	"JB",
	"JBE",
	"JC",
	"JE",
	"JECXZ",
	"JG",
	"JGE",
	"JL",
	"JLE",
	"JMP",
	"JNA",
	"JNAE",
	"JNB",
	"JNBE",
	"JNC",
	"JNE",
	"JNG",
	"JNGE",
	"JNL",
	"JNLE",
	"JNO",
	"JNP",
	"JNS",
	"JNZ",
	"JO",
	"JP",
	"JPE",
	"JPO",
	"JRCXZ",
	"JS",
	"JZ",
	"CALL",
}

// smallestUi returns the smallest unsigned integer representation possible for i
func smallestUi(i uint64) interface{} {
	n := bits.Len64(i)
	if n <= 8 {
		return uint8(i)
	}
	if n <= 16 {
		return uint16(i)
	}
	if n <= 32 {
		return uint32(i)
	}
	return i
	//if i > math.MaxUint64 {
	//	// Don't know what to do here. We'll just keep the integer unchanged.
	//	return i
	//}
	//return smallestInt(int64(i))
}

// this used to return either an int or a uint, depending on the range.
// However, all immediates in x86 appear to be sign-extended, meaning they are
// almost always treated as signed integers. For this reason, we should treat them that way here.
func smallestInt(i int64) interface{} {
	if i < 0 {
		// need a signed int.
		if i < math.MinInt32 {
			return i
		}
		if i < math.MinInt16 {
			return int32(i)
		}
		if i < math.MinInt8 {
			return int16(i)
		}
		return int8(i)
	}
	if i > math.MaxUint32 {
		return uint64(i)
	}
	if i > math.MaxUint16 {
		return uint32(i)
	}
	if i > math.MaxUint8 {
		return uint16(i)
	}
	return uint8(i)
}

// smallestI returns the smallest signed integer representation possible for i
func smallestI(i int64) interface{} {
	if i >= math.MaxInt32 {
		return int64(i)
	}
	if i >= math.MaxInt16 {
		return int32(i)
	}
	if i >= math.MaxInt8 {
		return int16(i)
	}
	if i >= math.MinInt8 {
		return int8(i)
	}
	if i >= math.MinInt16 {
		return int16(i)
	}
	if i >= math.MinInt32 {
		return int32(i)
	}
	return int64(i)
}

func SplitNSpace(s string, n int) []string {
	s = strings.ReplaceAll(s, "\t", " ")
	ss := strings.SplitN(s, " ", n)
	var k int
	for i := range ss {
		if ss[i] == "" {
			continue
		}
		ss[k] = ss[i]
		k++
	}
	ss = ss[:k]
	return ss
}

func SplitSpace(s string) []string {
	s = strings.ReplaceAll(s, "\t", " ")
	ss := strings.Split(s, " ")
	var k int
	for i := range ss {
		if ss[i] == "" {
			continue
		}
		ss[k] = ss[i]
		k++
	}
	ss = ss[:k]
	return ss
}

// sizeKeywords maps a size-prefix keyword to its bit width.
// Used for size-qualified memory operands: byte[reg+off], word[reg+off], dword[reg+off], qword[reg+off].
var sizeKeywords = []struct {
	prefix string
	bits   int
}{
	{"qword", 64},
	{"dword", 32},
	{"word", 16},
	{"byte", 8},
}

// parseSizePrefix checks whether s begins with a size keyword immediately followed
// by '[' (e.g. "qword[rsp+16]"). If so it returns the bit width and the bracket
// portion; otherwise it returns ok=false.
func parseSizePrefix(s string) (bits int, rest string, ok bool) {
	for _, kw := range sizeKeywords {
		p := kw.prefix + "["
		if strings.HasPrefix(s, p) {
			return kw.bits, s[len(kw.prefix):], true
		}
	}
	return 0, "", false
}

func ParseIndirect(o *gbasm.OFile, f *gbasm.Function, s string) (indirect any, err error) {
	//(base, index string, scale int, err error) {
	r := regexp.MustCompile(`\[([_a-zA-Z0-9]+)\s*(([+-])\s*([_x0-9a-zA-Z]+))?\s*(\*\s*([x0-9]+))?\]`)
	parts := r.FindStringSubmatch(s)
	base := parts[1]
	index := parts[4]
	scale := parts[6]

	var baser gbasm.Register
	var baseSym string // when set, base is a global symbol; baser is ignored
	if reg, err := gbasm.ParseReg(base); err == nil {
		baser = reg
	} else if a := f.AllocFor(base); a != nil {
		baser = a.Register()
	} else if o != nil && (o.Vars[base] != nil || o.Data[base] != nil) {
		// Global symbol: addressing becomes RIP-relative to this name,
		// with any index expression below baked into the relocation.
		baseSym = base
	} else {
		return nil, fmt.Errorf("Base %q was neither a register, a local variable, nor a known global symbol.", base)
	}

	// indirectFor builds the right Indirect flavor (register-relative
	// or RIP-relative-to-symbol) given the parsed offset. Used by the
	// three integer-index and no-index branches below.
	indirectFor := func(off int32) gbasm.Indirect {
		if baseSym != "" {
			return gbasm.Indirect{Symbol: baseSym, Off: off}
		}
		return gbasm.Indirect{Reg: baser, Off: off}
	}

	// figure out if index is an int or register
	if strings.HasPrefix(index, "0x") {
		i, err := strconv.ParseInt(strings.TrimPrefix(index, "0x"), 16, 32)
		if err != nil {
			return nil, err
		}
		if scale != "" {
			return nil, fmt.Errorf("Cannot index with scale with scale literal")
		}
		if parts[3] != "+" {
			return nil, fmt.Errorf("Cannot subtract hex literal in scale")
		}
		return indirectFor(int32(i)), nil
	}
	i, err := strconv.ParseInt(index, 10, 32)
	if err == nil {
		if scale != "" {
			return nil, fmt.Errorf("Cannot index with scale with scale literal")
		}
		if parts[3] == "-" {
			i = -i
		}
		return indirectFor(int32(i)), nil
	}

	if index == "" {
		if scale != "" {
			return nil, fmt.Errorf("Cannot have scale without index.")
		}
		return indirectFor(0), nil
	}

	// Not an integer index — a register-scaled index. Can't combine with
	// a RIP-relative base: x86-64 RIP addressing is "RIP + disp32" only,
	// with no base or index register.
	if baseSym != "" {
		return nil, fmt.Errorf("global symbol %q cannot be combined with a register index — x86-64 RIP-relative addressing takes a disp32 only.", baseSym)
	}
	var indexr gbasm.Register
	if reg, err := gbasm.ParseReg(index); err == nil {
		indexr = reg
	} else if a := f.AllocFor(index); a != nil {
		indexr = a.Register()
	} else {
		return nil, fmt.Errorf("Index was neither an integer, nor a register, nor a variable.")
	}

	scalei := 1
	if scale != "" {
		i, err := strconv.ParseInt(scale, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Scale must be an integer in [1, 2, 4, 8], but got %v", scale)
		}
		if i != 1 && i != 2 && i != 4 && i != 8 {
			return nil, fmt.Errorf("Scale must be an integer in [1, 2, 4, 8], but got %v", i)
		}
		scalei = int(i)
	}

	return gbasm.IndirectBaseIndexScale{
		Base:  baser,
		Index: indexr,
		Scale: scalei,
	}, nil
}

var out = flag.String("o", "", "Write the linked executable to this file")
var help = flag.Bool("h", false, "Print this help message.")

func main() {
	// Convert any panic from the assembler library into a clean fatal error.
	// Without this, volatile enforcement and other invariant panics produce
	// unreadable Go tracebacks.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "Fatal: %v\n", r)
			os.Exit(1)
		}
	}()

	flag.Parse()

	if *help {
		fmt.Printf("HELP MESSAGE\n")
		flag.PrintDefaults()
		return
	}

	if flag.NArg() < 1 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	var o *gbasm.OFile
	for fi := 0; fi < flag.NArg(); fi++ {
		fmt.Printf("Assembling %s\n", flag.Arg(fi))
		file, err := os.Open(flag.Arg(fi))
		if err != nil {
			fmt.Printf("Fatal: %s", err)
		}
		defer file.Close()

		var f *gbasm.Function
		//var locals map[string]*gbasm.Ralloc
		scanner := bufio.NewScanner(file)
		ln := 0
	lines:
		for scanner.Scan() {
			ln++
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "//") {
				continue
			}
			//fmt.Printf("INPUT %v\n", line)
			if strings.HasPrefix(line, "package") {
				pkgname := strings.TrimSpace(strings.TrimPrefix(line, "package"))
				if *out == "" {
					*out = pkgname + ".bo"
				}
				if o == nil {
					o, err = gbasm.NewOFile(*out, pkgname)
					if err != nil {
						fmt.Printf("Failed to create ofile: %s\n", err)
						os.Exit(1)
					}
				} else {
					if o.Pkgname != pkgname {
						fmt.Printf("Creating package %s: file %s has package name %s.\n", o.Pkgname, flag.Arg(fi), pkgname)
						os.Exit(1)
					}
				}
				continue
			} else if o == nil {
				fmt.Printf("Fatal: Need package declaration before anything else.")
				os.Exit(1)
			}
			if strings.HasPrefix(line, "struct") {
				// Multi-line directive:
				//   struct Name {
				//     field1 type1
				//     field2 type2
				//     ...
				//   }
				// Each field-line: first whitespace-delimited token is
				// the field name, everything after is the type string
				// (verbatim, so types containing spaces like '*mut Foo'
				// or 'byte[100]' survive). Whitespace-only lines and //
				// comments inside the body are ignored.
				rest := strings.TrimSpace(strings.TrimPrefix(line, "struct"))
				openIdx := strings.IndexByte(rest, '{')
				if openIdx < 0 {
					fmt.Printf("Fatal: struct directive: missing '{' on the same line, got: %q\n", line)
					os.Exit(1)
				}
				sname := strings.TrimSpace(rest[:openIdx])
				if sname == "" {
					fmt.Printf("Fatal: struct directive: missing name before '{'\n")
					os.Exit(1)
				}
				var fields []gbasm.FieldShape
				for {
					if !scanner.Scan() {
						fmt.Printf("Fatal: struct %s: unexpected EOF before '}'\n", sname)
						os.Exit(1)
					}
					ln++
					body := strings.TrimSpace(scanner.Text())
					if body == "" || strings.HasPrefix(body, "//") {
						continue
					}
					if body == "}" {
						break
					}
					// Field line: first whitespace token is name, rest is type.
					sp := strings.IndexAny(body, " \t")
					if sp < 0 {
						fmt.Printf("Fatal: struct %s: field line %q lacks a type\n", sname, body)
						os.Exit(1)
					}
					fname := body[:sp]
					ftype := strings.TrimSpace(body[sp+1:])
					if ftype == "" {
						fmt.Printf("Fatal: struct %s: field %s has empty type\n", sname, fname)
						os.Exit(1)
					}
					fields = append(fields, gbasm.FieldShape{Name: fname, Type: ftype})
				}
				if err := o.AddStruct(sname, fields); err != nil {
					fmt.Printf("Fatal: struct %s: %s\n", sname, err)
					os.Exit(1)
				}
				continue
			}
			if strings.HasPrefix(line, "function") {
				fname := strings.TrimSpace(strings.TrimPrefix(line, "function"))
				if strings.Contains(fname, " ") {
					fmt.Printf("Fatal: Function name \"%s\" contains a space.\n", fname)
					os.Exit(1)
				}
				f, err = o.NewFunction(flag.Arg(fi), ln, fname)
				if err != nil {
					fmt.Printf("Fatal: Failed to create function \"%s\": %s\n", fname, err)
					os.Exit(1)
				}
				//locals = make(map[string]*gbasm.Ralloc)
				continue
			}

			if strings.HasPrefix(line, "data") {
				f = nil // A new data declaration ends any current function
				parts := SplitNSpace(strings.TrimSpace(strings.TrimPrefix(line, "data")), 3)
				if len(parts) != 3 {
					fmt.Printf("Fatal: data declaration requires a name, type, and initial data, but got: %v\n", parts)
					os.Exit(1)
				}
				data, err := parseData(parts[2])
				if err != nil {
					fmt.Printf("Fatal: failed to parse data for data declaration %s: %v", parts[0], err)
					os.Exit(1)
				}
				o.AddData(parts[0], parts[1], data)
				continue
			}
			if strings.HasPrefix(line, "var") {
				f = nil // A new var declaration ends any current function
				parts := SplitNSpace(strings.TrimSpace(strings.TrimPrefix(line, "var")), 3)
				if len(parts) != 3 {
					fmt.Printf("Fatal: var declaration requires a name, type, and either a byte-count, a string literal, or a '{' block, but got: %v\n", parts)
					os.Exit(1)
				}
				var data []byte
				var relocs []gbasm.DataReloc
				switch {
				case strings.HasPrefix(parts[2], `"`):
					// String-literal form: "..." with escapes (\n, \\, \", \0, \xHH).
					d, err := parseData(parts[2])
					if err != nil {
						fmt.Printf("Fatal: failed to parse data for var %s: %v\n", parts[0], err)
						os.Exit(1)
					}
					data = d
				case parts[2] == "{":
					// Block form:
					//   var name type {
					//     bytes "<escaped>"
					//     reloc <offset> <symbol> <addend>
					//     ...
					//   }
					// `bytes` and `reloc` lines may appear in any order;
					// `bytes` is mandatory (use an explicit zero-filled
					// string literal if the var is otherwise empty),
					// `reloc` is optional and may appear multiple times.
					sawBytes := false
					for {
						if !scanner.Scan() {
							fmt.Printf("Fatal: var %s: unexpected EOF before '}'\n", parts[0])
							os.Exit(1)
						}
						ln++
						body := strings.TrimSpace(scanner.Text())
						if body == "" || strings.HasPrefix(body, "//") {
							continue
						}
						if body == "}" {
							break
						}
						switch {
						case strings.HasPrefix(body, "bytes"):
							if sawBytes {
								fmt.Printf("Fatal: var %s: duplicate 'bytes' line in block\n", parts[0])
								os.Exit(1)
							}
							payload := strings.TrimSpace(strings.TrimPrefix(body, "bytes"))
							if !strings.HasPrefix(payload, `"`) {
								fmt.Printf("Fatal: var %s: 'bytes' payload must be a string literal, got: %s\n", parts[0], payload)
								os.Exit(1)
							}
							d, err := parseData(payload)
							if err != nil {
								fmt.Printf("Fatal: var %s: failed to parse bytes payload: %v\n", parts[0], err)
								os.Exit(1)
							}
							data = d
							sawBytes = true
						case strings.HasPrefix(body, "reloc"):
							rp := SplitSpace(strings.TrimSpace(strings.TrimPrefix(body, "reloc")))
							if len(rp) != 3 {
								fmt.Printf("Fatal: var %s: 'reloc' requires three args (offset, symbol, addend), got: %v\n", parts[0], rp)
								os.Exit(1)
							}
							off, err := strconv.ParseUint(rp[0], 10, 32)
							if err != nil {
								fmt.Printf("Fatal: var %s: reloc offset must be a non-negative integer, got %q: %v\n", parts[0], rp[0], err)
								os.Exit(1)
							}
							addend, err := strconv.ParseInt(rp[2], 10, 64)
							if err != nil {
								fmt.Printf("Fatal: var %s: reloc addend must be an integer, got %q: %v\n", parts[0], rp[2], err)
								os.Exit(1)
							}
							relocs = append(relocs, gbasm.DataReloc{
								Offset: uint32(off),
								Symbol: rp[1],
								Addend: addend,
							})
						default:
							fmt.Printf("Fatal: var %s: unknown line in block: %q\n", parts[0], body)
							os.Exit(1)
						}
					}
					if !sawBytes {
						fmt.Printf("Fatal: var %s: block form requires a 'bytes' line\n", parts[0])
						os.Exit(1)
					}
					// Validate reloc offsets fit within the payload.
					for _, r := range relocs {
						if int(r.Offset)+8 > len(data) {
							fmt.Printf("Fatal: var %s: reloc at offset %d would write past end of %d-byte payload\n", parts[0], r.Offset, len(data))
							os.Exit(1)
						}
					}
				default:
					// Size form: an integer giving the number of zero-filled bytes.
					n, err := strconv.Atoi(parts[2])
					if err != nil {
						fmt.Printf("Fatal: var %s: third argument must be a string literal, an integer byte-count, or '{', got: %s\n", parts[0], parts[2])
						os.Exit(1)
					}
					if n < 0 {
						fmt.Printf("Fatal: var %s: byte-count cannot be negative: %d\n", parts[0], n)
						os.Exit(1)
					}
					data = make([]byte, n)
				}
				if err := o.AddVar(parts[0], parts[1], data); err != nil {
					fmt.Printf("Fatal: var %s: %s\n", parts[0], err)
					os.Exit(1)
				}
				if len(relocs) > 0 {
					o.Vars[parts[0]].Relocs = relocs
				}
				continue
			}

			// Handle Regular Line. Must be inside a function.
			if f == nil {
				fmt.Printf("Fatal: All assembly must be inside a function\n")
				os.Exit(1)
			}
			if strings.HasPrefix(line, "type") {
				ftype := strings.TrimSpace(strings.TrimPrefix(line, "type"))
				//fmt.Printf("DECL DECL DECL %s -> %s\n", f.Name, ftype)
				f.Type = ftype
				continue
			}
			if strings.HasPrefix(line, "local") {
				lnamesize := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "local")))
				if len(lnamesize) < 2 || len(lnamesize) > 3 {
					fmt.Printf("Fatal: Expect a local declaration to contain a name and bit size, and optionally a register, but have %v\n", lnamesize)
					os.Exit(1)
				}
				size, err := strconv.Atoi(lnamesize[1])
				if err != nil {
					fmt.Printf("Expected local size to be an integer, but have: %s\n", lnamesize[1])
					os.Exit(1)
				}
				l, err := f.NewLocal(lnamesize[0], size)
				if err != nil {
					fmt.Printf("Fatal: Failed to declare local %s: %s\n", lnamesize[1], err)
					os.Exit(1)
				}
				if len(lnamesize) == 3 {
					reg, err := gbasm.ParseReg(lnamesize[2])
					if err != nil {
						fmt.Printf("Fatal: Failed to use register %s: %s\n", lnamesize[2], err)
						os.Exit(1)
					}
					l.UseRegister(reg)
				}
				//locals[lnamesize[0]] = l
				continue
			}
			if strings.HasPrefix(line, "bytes") {
				bnamesize := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "bytes")))
				if len(bnamesize) < 2 || len(bnamesize) > 3 {
					fmt.Printf("Fatal: Expect a bytes declaration to contain a name and byte size, and optionally a register, but have %v\n", bnamesize)
					os.Exit(1)
				}
				size, err := strconv.Atoi(bnamesize[1])
				if err != nil {
					fmt.Printf("Expected bytes size to be an integer, but have: %s\n", bnamesize[1])
					os.Exit(1)
				}
				l, err := f.AllocBytes(bnamesize[0], size)
				if err != nil {
					fmt.Printf("Fatal: Failed to declare bytes %s: %s\n", bnamesize[1], err)
					os.Exit(1)
				}
				if len(bnamesize) == 3 {
					reg, err := gbasm.ParseReg(bnamesize[2])
					if err != nil {
						fmt.Printf("Fatal: Failed to use register %s: %s\n", bnamesize[2], err)
						os.Exit(1)
					}
					l.UseRegister(reg)
				}
				continue
			}
			if strings.HasPrefix(line, "volatile") {
				name := strings.TrimSpace(strings.TrimPrefix(line, "volatile"))
				if err := f.VolatileLocal(name); err != nil {
					fmt.Printf("Fatal: volatile %s: %s\n", name, err)
					os.Exit(1)
				}
				continue
			}
			if strings.HasPrefix(line, "forgetall") {
				f.ForgetAll()
				continue
			}
			if strings.HasPrefix(line, "forget") {
				name := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "forget")))
				if len(name) != 1 {
					fmt.Printf("Fatal: Expect a forget instruction to contain a name, but have %v\n", name)
					os.Exit(1)
				}
				err = f.Forget(name[0])
				if err != nil {
					fmt.Printf("Fatal: Failed to forget local %s: %s\n", name[0], err)
					os.Exit(1)
				}
				//locals[lnamesize[0]] = l
				continue
			}
			if strings.HasPrefix(line, "inreg") {
				// put a var in a specific reg
				ireg := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "inreg")))
				if len(ireg) != 2 {
					fmt.Printf("Fatal: Expect an inreg specify a variable and a register, but have: %v\n", ireg)
					os.Exit(1)
				}
				ra := f.AllocFor(ireg[0])
				if ra == nil {
					fmt.Printf("Fatal: No such var: %v\n", ireg[0])
					os.Exit(1)
				}
				reg, err := gbasm.ParseReg(ireg[1])
				if err != nil {
					fmt.Printf("Fatal: For inreg, cannot parse register %s: %v\n", ireg[1], err)
					os.Exit(1)
				}
				ra.UseRegister(reg)
				continue
			}
			if strings.HasPrefix(line, "use") {
				rname := strings.TrimSpace(strings.TrimPrefix(line, "use"))
				reg, err := gbasm.ParseReg(rname)
				if err != nil {
					fmt.Printf("Fatal: Failed to use register %s: %s\n", rname, err)
					os.Exit(1)
				}
				if !f.Use(reg) {
					fmt.Printf("Fatal: Failed to use register %s. Already in use.\n", rname)
					os.Exit(1)
				}
				continue
			}
			if strings.HasPrefix(line, "argi") {
				params := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "argi")))
				if len(params) < 2 || len(params) > 3 {
					fmt.Printf("Fatal: Expect an argi declaration to contain a name, index, and optional bit size, but have %v\n", line)
					os.Exit(1)
				}
				name := params[0]
				num, err := strconv.ParseInt(params[1], 10, 64)
				if err != nil {
					fmt.Printf("Fatal: Expect an argi declaration to contain a name register/offset, but have %v\n", line)
					os.Exit(1)
				}
				size := 64
				if len(params) == 3 {
					sz, err := strconv.ParseInt(params[2], 10, 64)
					if err != nil {
						fmt.Printf("Fatal: Expect argi size to be an integer, but have: %v\n", params[2])
						os.Exit(1)
					}
					size = int(sz)
				}
				if _, err := f.ArgI(name, int(num), size); err != nil {
					fmt.Printf("Fatal: Failed to mark arg %s: %s\n", name, err)
					os.Exit(1)
				}
				continue
			}
			if strings.HasPrefix(line, "arg") {
				params := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "arg")))
				if len(params) != 2 {
					fmt.Printf("Fatal: Expect an arg declaration to contain a name register/offset, but have %v\n", params)
					os.Exit(1)
				}
				name := params[0]
				if reg, err := gbasm.ParseReg(params[1]); err == nil {
					if _, err := f.Arg(name, reg); err != nil {
						fmt.Printf("Fatal: Failed to mark arg %s: %s\n", name, err)
						os.Exit(1)
					}
				} else if num, err := strconv.ParseInt(params[1], 10, 64); err == nil {
					if _, err := f.StackArg(name, int(num)); err != nil {
						fmt.Printf("Fatal: Failed to mark arg %s: %s\n", name, err)
						os.Exit(1)
					}
				} else {
					fmt.Printf("Fatal: Expect an arg declaration to contain a name register/offset, but have %v\n", params)
					os.Exit(1)
				}
				continue
			}
			if strings.HasPrefix(line, "evict") {
				params := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "evict")))
				if len(params) == 0 {
					f.EvictAll()
				}
				for _, p := range params {
					if reg, err := gbasm.ParseReg(p); err == nil {
						f.EvictReg(reg)
					} else {
						fmt.Printf("Fatal: Expect an evict argument to be a register, but have %v\n", p)
						os.Exit(1)
					}
				}
				continue
			}
			if strings.HasPrefix(line, "acquire") {
				// acquire will acquire a register for use by evicting any variables in it and marking it as in use
				params := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "acquire")))
				if len(params) == 0 {
					fmt.Printf("Fatal: Expect acquire to have register arguments, but have nothing.\n")
					os.Exit(1)
				}
				for _, p := range params {
					if reg, err := gbasm.ParseReg(p); err == nil {
						f.Acquire(reg)
					} else {
						fmt.Printf("Fatal: Expect an acquire argument to be a register, but have %v\n", p)
						os.Exit(1)
					}
				}
				continue
			}
			if strings.HasPrefix(line, "release") {
				// release will release a register acquired by "use" or "acquire"
				params := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "release")))
				if len(params) == 0 {
					fmt.Printf("Fatal: Expect release to have register arguments, but have nothing.\n")
					os.Exit(1)
				}
				for _, p := range params {
					if reg, err := gbasm.ParseReg(p); err == nil {
						f.Release(reg)
					} else {
						fmt.Printf("Fatal: Expect a release argument to be a register, but have %v\n", p)
						os.Exit(1)
					}
				}
				continue
			}

			if strings.HasPrefix(line, "label") {
				lname := strings.TrimSpace(strings.TrimPrefix(line, "label"))
				err := f.Label(lname)
				if err != nil {
					fmt.Printf("Fatal: Failed to set label %s: %s\n", lname, err)
					os.Exit(1)
				}
				continue
			}
			if line == "prologue" {
				err = f.Prologue()
				if err != nil {
					fmt.Printf("Fatal: Failed to write function prologue: %s\n", err)
					os.Exit(1)
				}
				continue
			}
			if line == "epilogue" {
				err = f.Epilogue()
				if err != nil {
					fmt.Printf("Fatal: Failed to write function epilogue: %s\n", err)
					os.Exit(1)
				}
				continue
			}

			parts := SplitSpace(line)
			instrUp := strings.ToUpper(parts[0])
			for _, i := range jumps {
				if i == instrUp {
					if len(parts) != 2 {
						fmt.Printf("Fatal: Jumps take exactly 1 argument, but got: %v\n", line)
						os.Exit(1)
					}
					// Indirect CALL: when the operand is a local (Ralloc)
					// or a register, emit the r/m64 form rather than the
					// rel32 relocation form. Function-pointer call sites
					// rely on this; everything else still goes through
					// the Jump (symbol-relocation) path.
					if instrUp == "CALL" {
						if alloc := f.AllocFor(parts[1]); alloc != nil {
							f.EvictForCall()
							if err := f.Instr("CALL", alloc); err != nil {
								fmt.Printf("Fatal: Instruction %v: %s\n", parts, err)
								os.Exit(1)
							}
							continue lines
						}
						if reg, err := gbasm.ParseReg(parts[1]); err == nil {
							f.EvictForCall()
							if err := f.Instr("CALL", reg); err != nil {
								fmt.Printf("Fatal: Instruction %v: %s\n", parts, err)
								os.Exit(1)
							}
							continue lines
						}
					}
					err = f.Jump(instrUp, parts[1])
					if err != nil {
						fmt.Printf("Fatal: Instruction %v: %s\n", parts, err)
						os.Exit(1)
					}
					continue lines
				}
			}

			args := make([]interface{}, len(parts)-1)
			for i := 1; i < len(parts); i++ {
				// Partial-of-alloc syntax: name:N where N is 8, 16, 32, or 64.
				// Refers to the low N bits of the named allocation.
				if colon := strings.IndexByte(parts[i], ':'); colon > 0 {
					name := parts[i][:colon]
					sizeStr := parts[i][colon+1:]
					if alloc := f.AllocFor(name); alloc != nil {
						bits, err := strconv.Atoi(sizeStr)
						if err == nil && (bits == 8 || bits == 16 || bits == 32 || bits == 64) {
							args[i-1] = &gbasm.RallocPartial{Ra: alloc, Bits: bits}
							continue
						}
						fmt.Printf("Fatal: bad partial size in %q: must be 8, 16, 32, or 64\n", parts[i])
						os.Exit(1)
					}
				}

				if alloc := f.AllocFor(parts[i]); alloc != nil {
					args[i-1] = alloc //alloc.Register()
					continue
				}
				if v := o.VarFor(parts[i]); v != nil {
					//panic(fmt.Sprintf("VAR FOR %s\n", parts[i]))
					args[i-1] = v
					continue
				}

				if reg, err := gbasm.ParseReg(parts[i]); err == nil {
					args[i-1] = reg
					continue
				}

				if bits, rest, ok := parseSizePrefix(parts[i]); ok {
					ind, err := ParseIndirect(o, f, rest)
					if err != nil {
						fmt.Printf("Fatal: Failed to parse indirection: %v\n", err)
						os.Exit(1)
					}
					if indirect, ok := ind.(gbasm.Indirect); ok {
						indirect.Size = bits
						args[i-1] = indirect
					} else {
						args[i-1] = ind
					}
					continue
				}

				if strings.HasPrefix(parts[i], "[") {
					ind, err := ParseIndirect(o, f, parts[i])
					if err != nil {
						fmt.Printf("Fatal: Failed to parse indirection: %v\n", err)
						os.Exit(1)
					}
					args[i-1] = ind
					continue
				}

				if strings.HasPrefix(parts[i], "0x") {
					num, err := strconv.ParseUint(strings.TrimPrefix(parts[i], "0x"), 16, 64)
					if err != nil {
						fmt.Printf("Fatal: Failed to parse hex %s: %s\n", parts[i], err)
						os.Exit(1)
					}
					//fmt.Printf("%v -> Parsed %s into %d (%X)(%v)\n", parts, parts[i], smallestUi(num), smallestUi(num), reflect.TypeOf(smallestUi(num)).String())
					args[i-1] = smallestUi(num)
					continue
				}
				if num, err := strconv.ParseInt(parts[i], 10, 64); err == nil {
					args[i-1] = smallestInt(num)
					continue
				}
				// Try unsigned for values larger than INT64_MAX.
				if num, err := strconv.ParseUint(parts[i], 10, 64); err == nil {
					args[i-1] = smallestUi(num)
					continue
				}
				// Unresolved identifier: treat as an external symbol
				// reference (e.g. a function name like "pkg.fn"). The
				// encoder turns *Var operands into RIP-relative
				// references with a relocation against ot.Name, which
				// the linker resolves to whichever symbol matches —
				// data or code. Only fields used by that path need to
				// be set; VType/Val stay empty.
				if isIdentifier(parts[i]) {
					args[i-1] = &gbasm.Var{Name: parts[i]}
					continue
				}
				args[i-1] = parts[i]
			}
			//fmt.Printf("INSTRUP: %#v\n args: %#v\n", instrUp, args)
			err := f.Instr(instrUp, args...)
			if err != nil {
				fmt.Printf("Fatal: Instruction %v: %s\n", parts, err)
				os.Exit(1)
			}
		}
	}
	if o == nil {
		fmt.Printf("Fatal: No non-empty files found.\n")
		os.Exit(1)
	}

	err := o.Output()
	if err != nil {
		fmt.Printf("Fatal: Failed to write object file: %s\n", err)
		os.Exit(1)
	}

	for k, f := range o.Funcs {
		//fmt.Printf("Function %s\n", k)
		_, err := f.Body()
		if err != nil {
			fmt.Printf("Can't get function %s body: %s\n", k, err)
			os.Exit(1)
		}
		// 		for _, b := range text {
		// 			fmt.Printf("%02x ", b)
		// 		}
		// 		fmt.Printf("\n")
	}

	// 	// This part should be moved to the linker, but for now we'll put it here for testing.
	// 	text := gbasm.Link([]*gbasm.OFile{o})
	// 	for _, b := range text {
	// 		fmt.Printf("%02x ", b)
	// 	}
	// 	fmt.Printf("\n")
	// 	err = gbasm.WriteExe("out.o", gbasm.MACHO, text)
	// 	if err != nil {
	// 		log.Fatalf("Failed to write exe: %s", err)
	// 	}
}

// isIdentifier reports whether s is a syntactically plausible symbol
// name — leading letter or underscore, then letters/digits/underscores
// with optional dot-separated qualifier (e.g. "pkg.func"). Used to
// distinguish unresolved-symbol arguments from accidental fallthrough.
func isIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, r := range s {
		if r == '.' {
			if i == 0 || i == len(s)-1 {
				return false
			}
			continue
		}
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'
		if i == 0 {
			if !isAlpha {
				return false
			}
		} else if !isAlpha && !isDigit {
			return false
		}
	}
	return true
}

func parseData(s string) ([]byte, error) {
	if strings.HasPrefix(s, `"`) {
		return parseString(s)
	}
	return nil, fmt.Errorf("Could not parse '%s'", s)
}

func parseString(s string) ([]byte, error) {
	if !strings.HasPrefix(s, `"`) {
		return nil, fmt.Errorf("Expected string to begin with '\"'")
	}
	var bs bytes.Buffer
	for s = s[1:]; len(s) > 0; s = s[1:] {
		if s[0] == '\\' {
			if len(s) < 2 {
				return nil, errors.New("dangling '\\' at end of string literal")
			}
			switch s[1] {
			case 'n':
				bs.WriteByte('\n')
			case 'r':
				bs.WriteByte('\r')
			case 't':
				bs.WriteByte('\t')
			case '\\':
				bs.WriteByte('\\')
			case '"':
				bs.WriteByte('"')
			case '0':
				bs.WriteByte(0)
			case 'x':
				// \xHH — exactly two hex digits, producing the byte HH.
				// Compiler-emitted globals use this to encode arbitrary
				// binary content (struct payloads, integer literals, etc.)
				// inside the existing string-literal var directive.
				if len(s) < 4 {
					return nil, errors.New("\\x escape requires two hex digits")
				}
				b, err := strconv.ParseUint(s[2:4], 16, 8)
				if err != nil {
					return nil, fmt.Errorf("\\x escape: %v", err)
				}
				bs.WriteByte(byte(b))
				s = s[2:] // additionally skip the two hex digits
			default:
				return nil, fmt.Errorf("unknown escape sequence: \\%c", s[1])
			}
			s = s[1:] // skip the escape's main char; loop's s=s[1:] skips the backslash
		} else if s[0] == '"' {
			break
		} else {
			bs.WriteByte(s[0])
		}
	}
	if len(s) != 1 || s[0] != '"' {
		return nil, errors.New("String did not end with a double quote.")
	}
	return bs.Bytes(), nil
}
