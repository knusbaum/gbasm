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

func ParseIndirect(f *gbasm.Function, s string) (indirect any, err error) {
	//(base, index string, scale int, err error) {
	r := regexp.MustCompile(`\[([_a-zA-Z0-9]+)\s*(([+-])\s*([_x0-9a-zA-Z]+))?\s*(\*\s*([x0-9]+))?\]`)
	parts := r.FindStringSubmatch(s)
	base := parts[1]
	index := parts[4]
	scale := parts[6]

	var baser gbasm.Register
	if reg, err := gbasm.ParseReg(base); err == nil {
		baser = reg
	} else if a := f.AllocFor(base); a != nil {
		baser = a.Register()
	} else {
		return nil, fmt.Errorf("Base was neither an integer, nor a register, nor a variable.")
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
		return gbasm.Indirect{
			Reg: baser,
			Off: int32(i),
		}, nil
	}
	i, err := strconv.ParseInt(index, 10, 32)
	if err == nil {
		if scale != "" {
			return nil, fmt.Errorf("Cannot index with scale with scale literal")
		}
		if parts[3] == "-" {
			i = -i
		}
		return gbasm.Indirect{
			Reg: baser,
			Off: int32(i),
		}, nil
	}

	if index == "" {
		if scale != "" {
			return nil, fmt.Errorf("Cannot have scale without index.")
		}
		return gbasm.Indirect{
			Reg: baser,
		}, nil
	}

	// Not an integer index
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
			fmt.Printf("INPUT %v\n", line)
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
					fmt.Printf("Fatal: var declaration requires a name, type, and initial data, but got: %v\n", parts)
					os.Exit(1)
				}
				data, err := parseData(parts[2])
				if err != nil {
					fmt.Printf("Fatal: failed to parse data for data declaration %s: %v", parts[0], err)
					os.Exit(1)
				}
				//fmt.Printf("##########\n\n##########\nGot Data: [%s]\n##########\n\n##########\n", string(data))
				o.AddVar(parts[0], parts[1], data)
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
				if len(params) != 2 {
					fmt.Printf("Fatal: Expect an argi declaration to contain a name register/offset, but have %v\n", line)
					os.Exit(1)
				}
				name := params[0]
				if num, err := strconv.ParseInt(params[1], 10, 64); err == nil {
					if _, err := f.ArgI(name, int(num)); err != nil {
						fmt.Printf("Fatal: Failed to mark arg %s: %s\n", name, err)
						os.Exit(1)
					}
				} else {
					fmt.Printf("Fatal: Expect an argi declaration to contain a name register/offset, but have %v\n", line)
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

				if strings.HasPrefix(parts[i], "[") {
					ind, err := ParseIndirect(f, parts[i])
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
					//fmt.Printf("%v -> Parsed %s into %d (%X)(%v)\n", parts, parts[i], smallestInt(num), smallestInt(num), reflect.TypeOf(smallestInt(num)).String())
					args[i-1] = smallestInt(num)
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

func parseData(s string) ([]byte, error) {
	if strings.HasPrefix(s, `"`) {
		return parseString(s)
	}
	return nil, fmt.Errorf("Could not parse '%s'", s)
}

func parseString(s string) ([]byte, error) {
	//fmt.Printf("Parsing string [%s]\n", s)
	if !strings.HasPrefix(s, `"`) {
		return nil, fmt.Errorf("Expected string to begin with '\"'")
	}
	//var closed bool
	var bs bytes.Buffer
	for s = s[1:]; len(s) > 0; s = s[1:] {
		//fmt.Printf("Parsing string [%s]\n", s)
		if s[0] == '\\' {
			switch s[1] {
			case 'n':
				bs.Write([]byte("\n"))
			case '\\':
				bs.Write([]byte("\\"))
			case '"':
				bs.Write([]byte("\""))
			case '0':
				bs.Write([]byte{0})
			}
			s = s[1:] // Skip the slash and the escaped character will be skipped by the loop.
		} else if s[0] == '"' {
			//closed = true
			break
		} else {
			bs.Write([]byte{s[0]})
		}
	}
	if len(s) != 1 || s[0] != '"' {
		return nil, errors.New("String did not endwith a double quote.")
	}
	return bs.Bytes(), nil
}
