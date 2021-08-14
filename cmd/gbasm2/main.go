package main

import (
	"bytes"
	"fmt"
	"io"
	"log"

	"github.com/knusbaum/gbasm2"
)

func main() {
	test4()
}

/** Test 4 - Testing recursive functions with fibonacci **/

func fib(o *gbasm2.OFile) {
	f, err := o.NewFunction("UNKNOWN", 0, "fib")
	if err != nil {
		log.Fatalf("Failed to create function: %s", err)
	}

	f.Prologue()
	if !f.Use(gbasm2.R_EDI) {
		log.Fatalf("Failed to use EDI")
	}
	if !f.Use(gbasm2.R_RAX) {
		log.Fatalf("Failed to use RAX")
	}
	n, err := f.NewLocal("n", 32)
	if err != nil {
		log.Fatalf("Failed to allocate a local: %s", err)
	}
	f1, err := f.NewLocal("f1", 32)
	if err != nil {
		log.Fatalf("Failed to allocate a local: %s", err)
	}
	f2, err := f.NewLocal("f2", 32)
	if err != nil {
		log.Fatalf("Failed to allocate a local: %s", err)
	}

	f.Instr("MOV", n.Register(), gbasm2.R_EDI) // EDI is the first argument.
	f.Instr("CMP", n.Register(), uint32(3))
	f.Jump("JGE", "recursive")
	f.Instr("MOV", gbasm2.R_EAX, uint32(1))
	f.Jump("JMP", "end")

	f.Label("recursive")
	// fib(n - 1)
	f.Instr("MOV", gbasm2.R_EDI, n.Register())
	f.Instr("SUB", gbasm2.R_EDI, uint32(1))
	f.Jump("CALL", "fib") // fib(n - 1)
	f.Instr("MOV", f1.Register(), gbasm2.R_EAX)

	// fib(n - 2)
	f.Instr("MOV", gbasm2.R_EDI, n.Register())
	f.Instr("SUB", gbasm2.R_EDI, uint32(2))
	f.Jump("CALL", "fib") // fib(n - 2)
	f.Instr("MOV", f2.Register(), gbasm2.R_EAX)

	// fib(n-1) + fib(n-2)
	// TODO: Implement register locking.
	//       Here it's possible for f1.Register() to evict f2 and then for f2.Register() to evict f1 before
	//       the ADD is written.
	log.Printf("LOCAL FOR F1: %v, LOCAL FOR F2: %v", f1.Register(), f2.Register())
	f.Instr("ADD", f1.Register(), f2.Register())
	f.Instr("MOV", gbasm2.R_EAX, f1.Register())

	f.Label("end")
	f.Epilogue()
	f.Instr("RET")
}

func test4() {
	o, err := gbasm2.NewOFile("test.o", "testpkg")
	if err != nil {
		log.Fatalf("Failed to create ofile: %s", err)
	}
	fib(o)
	start(o)
	f, err := o.NewFunction("UNKNOWN", 0, "main")
	if err != nil {
		log.Fatalf("Failed to create function: %s", err)
	}
	f.Instr("MOV", gbasm2.R_EDI, uint32(8))
	f.Jump("CALL", "fib")
	f.Instr("RET")

	text := gbasm2.Link([]*gbasm2.OFile{o})
	for _, b := range text {
		fmt.Printf("%02x ", b)
	}
	fmt.Printf("\n")
	err = gbasm2.WriteExe("out.o", gbasm2.MACHO, text)
	if err != nil {
		log.Fatalf("Failed to write exe: %s", err)
	}
}

/** Test 3 - Testing function calling, ofile reading/writing and linking into an executable. **/

func test3() {
	o, err := gbasm2.NewOFile("test.o", "testpkg")
	if err != nil {
		log.Fatalf("Failed to create ofile: %s", err)
	}
	start(o)
	callFunc(o)
	mainFunc(o)
	log.Printf("Writing ofile.")
	err = o.Output()
	if err != nil {
		log.Fatalf("Failed to write ofile: %s", err)
	}
	log.Printf("##########")
	log.Printf("Reading ofile")
	o, err = gbasm2.ReadOFile("test.o")
	if err != nil {
		log.Fatalf("Failed to read ofile: %s", err)
	}

	text := gbasm2.Link([]*gbasm2.OFile{o})
	for _, b := range text {
		fmt.Printf("%02x ", b)
	}
	fmt.Printf("\n")
	err = gbasm2.WriteExe("out.o", gbasm2.MACHO, text)
	if err != nil {
		log.Fatalf("Failed to write exe: %s", err)
	}
}

func callFunc(o *gbasm2.OFile) {
	f, err := o.NewFunction("UNKNOWN", 0, "count")
	if err != nil {
		log.Fatalf("Failed to create function: %s", err)
	}

	f.Prologue()
	local, err := f.NewLocal("counter", 8)
	if err != nil {
		log.Fatalf("Failed to allocate a local: %s", err)
	}
	f.Instr("MOV", local.Register(), uint8(10))
	f.Label("loop1")
	f.Instr("SUB", local.Register(), uint8(1))
	f.Instr("TEST", local.Register(), local.Register())
	f.Jump("JNZ", "loop1")
	f.Instr("MOV", gbasm2.R_RAX, uint64(0x00))
	f.Epilogue()
	f.Instr("RET")
}

func mainFunc(o *gbasm2.OFile) {
	f, err := o.NewFunction("UNKNOWN", 0, "main")
	if err != nil {
		log.Fatalf("Failed to create function: %s", err)
	}
	f.Prologue()
	f.Jump("CALL", "count")
	f.Epilogue()
	f.Instr("RET")
}

func start(o *gbasm2.OFile) {
	f, err := o.NewFunction("UNKNOWN", 0, "start")
	if err != nil {
		log.Fatalf("Failed to create function: %s", err)
	}
	f.Jump("CALL", "main")
	f.Instr("MOV", gbasm2.R_RDI, gbasm2.R_RAX)
	f.Instr("MOV", gbasm2.R_RAX, uint64(0x2000001))
	f.Instr("SYSCALL")
}

/** End Test3 **/

/** Test2 - Testing basic function encoding **/

func test2() {
	o, err := gbasm2.NewOFile("test.o", "testpkg")
	if err != nil {
		log.Fatalf("Failed to create ofile: %s", err)
	}

	f, err := o.NewFunction("UNKNOWN", 0, "count")
	if err != nil {
		log.Fatalf("Failed to create function: %s", err)
	}

	f.Prologue()
	local, err := f.NewLocal("counter", 8)
	if err != nil {
		log.Fatalf("Failed to allocate a local: %s", err)
	}
	f.Instr("MOV", local.Register(), uint8(100))
	f.Label("loop1")
	f.Instr("SUB", local.Register(), uint8(1))
	f.Instr("TEST", local.Register(), local.Register())
	f.Jump("JNZ", "loop1")
	f.Epilogue()
	f.Instr("RET")
	bs, err := f.Body()
	if err != nil {
		log.Fatalf("Failed to write body: %s", err)
	}
	for _, b := range bs {
		fmt.Printf("%02x ", b)
	}
	fmt.Printf("\n###\n")
	err = o.Output()
	if err != nil {
		log.Fatalf("Failed to write ofile: %s", err)
	}
}

/** End Test2 **/

/** Test1 - Testing the Asm encoder **/

func test1() {
	a, err := gbasm2.LoadAsm(gbasm2.AMD64)
	if err != nil {
		log.Fatalf("Failed to parse: %s", err)
	}

	var bs bytes.Buffer
	encode(a, &bs, "PUSH", gbasm2.R_RBP)
	encode(a, &bs, "MOV", gbasm2.R_RBP, gbasm2.R_RSP)
	encode(a, &bs, "MOV", gbasm2.R_AX, uint16(100))
	encode(a, &bs, "SUB", gbasm2.R_AX, uint16(1))
	encode(a, &bs, "TEST", gbasm2.R_AX, gbasm2.R_AX)
	encode(a, &bs, "JNZ", int8(-8))

	for _, b := range bs.Bytes() {
		fmt.Printf("%02x ", b)
	}
	fmt.Printf("\n")
}

func encode(a *gbasm2.Asm, w io.Writer, instr string, op ...interface{}) {
	err := a.Encode(w, instr, op...)
	if err != nil {
		log.Fatal(err)
	}
}

/** End Test1 **/
