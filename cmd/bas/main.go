package main

import (
	"bufio"
	"bytes"
	"errors"
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

func ParseIndirect(s string) (base string, offset int32, err error) {
	r := regexp.MustCompile(`\[([_a-zA-Z0-9]+)([+-][x0-9]+)?\]`)
	parts := r.FindStringSubmatch(s)
	//fmt.Printf("HAVE PARTS: %#v from %s\n", parts, s)
	// 	reg, err = gbasm.ParseReg(parts[1])
	// 	if err != nil {
	// 		return
	// 	}
	base = parts[1]
	if parts[2] != "" {
		var o int64
		if strings.HasPrefix(parts[2], "0x") {
			o, err = strconv.ParseInt(strings.TrimPrefix(parts[2], "0x"), 16, 32)
			offset = int32(o)
			return
		}
		o, err = strconv.ParseInt(strings.TrimPrefix(parts[2], "0x"), 10, 32)
		offset = int32(o)
		return
	}
	return
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	var out string

	var o *gbasm.OFile
	for fi := 1; fi < len(os.Args); fi++ {
		fmt.Printf("Assembling %s\n", os.Args[fi])
		file, err := os.Open(os.Args[fi])
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
			fmt.Printf("%v\n", line)
			if strings.HasPrefix(line, "package") {
				pkgname := strings.TrimSpace(strings.TrimPrefix(line, "package"))
				if out == "" {
					out = pkgname + ".bo"
				}
				if o == nil {
					o, err = gbasm.NewOFile(out, pkgname)
					if err != nil {
						fmt.Printf("Failed to create ofile: %s\n", err)
						os.Exit(1)
					}
				} else {
					if o.Pkgname != pkgname {
						fmt.Printf("Creating package %s: file %s has package name %s.\n", o.Pkgname, os.Args[fi], pkgname)
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
				f, err = o.NewFunction(os.Args[fi], ln, fname)
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
				if len(lnamesize) != 2 {
					fmt.Printf("Fatal: Expect a local declaration to contain a name and bit size, but have %v\n", lnamesize)
					os.Exit(1)
				}
				size, err := strconv.Atoi(lnamesize[1])
				if err != nil {
					fmt.Printf("Expected local size to be an integer, but have: %s\n", lnamesize[1])
					os.Exit(1)
				}
				_, err = f.NewLocal(lnamesize[0], size)
				if err != nil {
					fmt.Printf("Fatal: Failed to declare local %s: %s\n", lnamesize[1], err)
					os.Exit(1)
				}
				//locals[lnamesize[0]] = l
				continue
			}
			if strings.HasPrefix(line, "bytes") {
				bnamesize := SplitSpace(strings.TrimSpace(strings.TrimPrefix(line, "bytes")))
				if len(bnamesize) != 2 {
					fmt.Printf("Fatal: Expect a bytes declaration to contain a name and byte size, but have %v\n", bnamesize)
					os.Exit(1)
				}
				size, err := strconv.Atoi(bnamesize[1])
				if err != nil {
					fmt.Printf("Expected bytes size to be an integer, but have: %s\n", bnamesize[1])
					os.Exit(1)
				}
				_, err = f.AllocBytes(bnamesize[0], size)
				if err != nil {
					fmt.Printf("Fatal: Failed to declare bytes %s: %s\n", bnamesize[1], err)
					os.Exit(1)
				}
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
					base, offset, err := ParseIndirect(parts[i])
					if err != nil {
						fmt.Printf("Fatal: Failed to parse indirection %s: %s\n", parts[i], err)
						os.Exit(1)
					}
					if reg, err := gbasm.ParseReg(base); err == nil {
						args[i-1] = gbasm.Indirect{Reg: reg, Off: offset}
					} else if v := f.AllocFor(base); v != nil {
						args[i-1] = gbasm.Indirect{Reg: v.Register(), Off: offset} // Do we ever need size? , Size: v.?()}
					} else {
						panic(fmt.Sprintf("don't know what %s is.", parts[i]))
					}
					continue
				}

				if strings.HasPrefix(parts[i], "0x") {
					num, err := strconv.ParseUint(strings.TrimPrefix(parts[i], "0x"), 16, 64)
					if err != nil {
						fmt.Printf("Fatal: Failed to parse hex %s: %s\n", parts[i], err)
						os.Exit(1)
					}
					args[i-1] = smallestUi(num)
					continue
				}
				if num, err := strconv.ParseInt(parts[i], 10, 64); err == nil {
					//fmt.Printf("%v -> Parsed %s into %d (%X)\n", parts, parts[i], smallestI(num), smallestI(num))
					args[i-1] = smallestI(num)
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
