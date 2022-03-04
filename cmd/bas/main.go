package main

import (
	"bufio"
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

// smallestUi returns the smallestUi integer representation possible for i
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

// smallestUi returns the smallestUi integer representation possible for i
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

func ParseIndirect(s string) (reg gbasm.Register, offset int32, err error) {
	r := regexp.MustCompile(`\[([a-zA-Z0-9]+)([+-][x0-9]+)?\]`)
	parts := r.FindStringSubmatch(s)
	fmt.Printf("HAVE PARTS: %#v\n", parts)
	reg, err = gbasm.ParseReg(parts[1])
	if err != nil {
		return
	}
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
		fmt.Printf("Opening %s\n", os.Args[fi])
		file, err := os.Open(os.Args[fi])
		if err != nil {
			fmt.Printf("Fatal: %s", err)
		}
		defer file.Close()

		var f *gbasm.Function
		var locals map[string]*gbasm.Ralloc
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
				f, err = o.NewFunction(os.Args[1], ln, fname)
				if err != nil {
					fmt.Printf("Fatal: Failed to create function \"%s\": %s\n", fname, err)
					os.Exit(1)
				}
				locals = make(map[string]*gbasm.Ralloc)
				continue
			}

			// Handle Regular Line. Must be inside a function.
			if f == nil {
				fmt.Printf("Fatal: All assembly must be inside a function\n")
				os.Exit(1)
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
				l, err := f.NewLocal(lnamesize[0], size)
				if err != nil {
					fmt.Printf("Fatal: Failed to declare local %s: %s\n", lnamesize[1], err)
					os.Exit(1)
				}
				locals[lnamesize[0]] = l
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
						fmt.Printf("Fatal: Jumps take exactly 1 argument.")
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
				if local, ok := locals[parts[i]]; ok {
					args[i-1] = local.Register()
					continue
				}

				if reg, err := gbasm.ParseReg(parts[i]); err == nil {
					args[i-1] = reg
					continue
				}

				if strings.HasPrefix(parts[i], "[") {
					reg, offset, err := ParseIndirect(parts[i])
					if err != nil {
						fmt.Printf("Fatal: Failed to parse indirection %s: %s\n", parts[i], err)
						os.Exit(1)
					}
					args[i-1] = gbasm.Indirect{Reg: reg, Off: offset}
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
					fmt.Printf("%v -> Parsed %s into %d (%X)\n", parts, parts[i], smallestI(num), smallestI(num))
					args[i-1] = smallestI(num)
					continue
				}
				args[i-1] = parts[i]
			}
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
		fmt.Printf("Function %s\n", k)
		text, err := f.Body()
		if err != nil {
			fmt.Printf("Can't get function %s body: %s\n", k, err)
			os.Exit(1)
		}
		for _, b := range text {
			fmt.Printf("%02x ", b)
		}
		fmt.Printf("\n")
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
