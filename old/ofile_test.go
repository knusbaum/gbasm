package gbasm

import (
	"testing"

	"github.com/davecgh/go-spew/spew"
)

func TestReadWrite(t *testing.T) {
	o := NewOFile("test", "testpkg")
	err := o.Type("int", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = o.Var("foo", "int")
	if err != nil {
		t.Fatal(err)
	}
	err = o.Data("bar", "int")
	if err != nil {
		t.Fatal(err)
	}
	f, err := o.NewFunction("ofile_test.go", 19, "test1", &Var{"x", "int"})
	if err != nil {
		t.Fatal(err)
	}
	// 	f.Move(EAX, EBX)
	// 	f.Move(EBX, ECX)
	// 	f.Move(AL, AH)
	// 	f.Move(BH, BL)
	// 	f.Move(RAX, RCX)
	// 	f.Move(Indirect{Reg: RAX}, RBX)
	// 	f.Move(Indirect{Reg: RAX}, EBX)
	// 	f.Move(Indirect{Reg: RAX}, BX)
	// 	f.Move(Indirect{Reg: RAX}, AH)
	// 	f.Move(Indirect{Reg: RAX}, AL)
	// 	f.Move(EBP, EAX)
	// 	f.Move(EAX, EBP)
	// 	f.Move(ESP, EBP)
	// 	f.Move(EBP, ESP)
	// 	f.Move(Indirect{Reg: RBP}, ESP)
	// 	f.Move(Indirect{Reg: RBP, Off: 0x10}, ESP)
	// 	f.Move(Indirect{Reg: RBP, Off: 0x7EADBEEF}, ESP)
	// 	f.Move(RSP, Indirect{Reg: RBP})
	// 	f.Move(ESP, Indirect{Reg: RBP})
	// 	f.Move(SP, Indirect{Reg: RBP})
	// 	f.Move(RSP, Indirect{Reg: RBP, Off: 0x10})
	// 	f.Move(ESP, Indirect{Reg: RBP, Off: 0x10})
	// 	f.Move(SP, Indirect{Reg: RBP, Off: 0x10})
	// 	f.Move(RSP, Indirect{Reg: RBP, Off: 0x7EADBEEF})
	// 	f.Move(ESP, Indirect{Reg: RBP, Off: 0x7EADBEEF})
	// 	f.Move(SP, Indirect{Reg: RBP, Off: 0x7EADBEEF})
	// 	f.Move(EAX, Indirect{Reg: DS, Off: 0xFEED})
	// 	f.Move(Indirect{Reg: DS, Off: 0xFEED}, RAX)
	// 	f.Move(BaseIndexScale{Base: RAX, Index: RBX, Scale: 2}, RCX)
	// 	f.Move(RCX, BaseIndexScale{Base: RAX, Index: RBX, Scale: 2})
	// 	f.Move(RDX, BaseIndexScale{Base: RAX, Index: RBX, Scale: 8})
	// 	f.Move(EBP, Indirect{Reg: RSP})
	// 	f.Move(Indirect{Reg: RSP}, EBP)
	// 	f.Move(ESP, Indirect{Reg: RBP})
	// 	f.Move(Indirect{Reg: RBP}, ESP)
	// 	f.Add(AL, AH)
	// 	f.Add(AL, BL)
	// 	f.Add(AH, BH)
	// 	f.Add(EAX, EBX)
	// 	f.Add(EAX, Indirect{Reg: RBX})
	// 	f.Add(Indirect{Reg: RBX}, EAX)
	// 	f.Add(uint16(10), AX)
	// 	f.Add(uint32(1), EAX)
	// 	f.Add(uint32(1), RAX)
	// 	f.Add(1, RAX)
	// 	f.Add(10101010, RBX)
	// 	f.Add(-20000, RCX)
	// 	f.Add(1, EAX)
	// 	f.Add(10101010, EBX)
	// 	f.Add(-20000, ECX)
	// 	f.Add(1, AX)
	// 	f.Add(10101010, EBX)
	// 	f.Add(-20000, ECX)
	// 	f.Move(uint64(0xCAFEBABEDEADBEEF), RAX)
	// 	f.Move(0x1000, ECX)
	// 	f.Move(0x1000, CX)
	// 	f.Move(0x10, CH)
	f.Sub(0x10, EBX)
	f.Add(0x10, EBX)
	err = o.Output()
	if err != nil {
		t.Fatal(err)
	}
	o2, err := ReadOFile("test")
	if err != nil {
		t.Fatal(err)
	}
	spew.Dump(o2)
}
