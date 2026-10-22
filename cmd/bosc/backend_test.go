package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBackendPreservesMetadataAndBindingNames(t *testing.T) {
	input := []byte("package p\nvar record byte[3] {\n\tbytes \"rax\"\n}\nfunction f\n\tlocal al 64\n\tmov al 42\n\tmov rax al\n\tshl al\n\tret\n")
	var original bytes.Buffer
	if err := (amd64Backend{}).Emit(&original, input); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Bytes(), input) {
		t.Fatal("amd64 backend changed output")
	}
	var arm bytes.Buffer
	if err := (arm64Backend{}).Emit(&arm, input); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"target darwin/arm64", "\tbytes \"rax\"", "local __arm_al 64", "mov x8 __arm_al", "shl __arm_al x3", "mov x0 x8\n\tret"} {
		if !strings.Contains(arm.String(), want) {
			t.Errorf("missing %q in:\n%s", want, arm.String())
		}
	}
}
