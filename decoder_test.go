package gbasm

import (
	"bytes"
	"fmt"
	"log"
	"testing"
)

func TestDecoder(t *testing.T) {
	is, err := DecodeFile("x86_64.xml")
	//_, err := DecodeFile("x86_64.xml")
	if err != nil {
		log.Fatalf("Failed to parse: %s", err)
	}
	Dump(is)
}

func TestEncoder(t *testing.T) {
	a, err := ParseFile("x86_64.xml")
	if err != nil {
		log.Fatalf("Failed to parse: %s", err)
	}

	var bs bytes.Buffer
	err = a.Encode(&bs, "MOV", R_AL, R_AH)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "MOV", R_RAX, R_RBX)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "ADD", R_AX, R_BX)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "ADD", R_EAX, R_EBX)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "ADD", R_RAX, R_RBX)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "ADD", R_RAX, R_RSP)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "ADD", R_EAX, R_ESP)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "CMP", R_AX, uint16(10))
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "CMP", R_AX, R_DX)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Encode(&bs, "JE", int8(-10))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs.Bytes() {
		fmt.Printf("%02x ", b)
	}
	fmt.Printf("\n")
	//log.Printf("BS: %v\n", bs.Bytes())
}
