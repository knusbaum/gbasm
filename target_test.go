package gbasm

import (
	"bytes"
	"strings"
	"testing"
)

func TestTargetsAndNativeObjectRoundTrip(t *testing.T) {
	for _, name := range []string{"", "linux/amd64", "darwin/arm64"} {
		if _, err := ParseTarget(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ParseTarget("darwin/amd64"); err == nil {
		t.Fatal("accepted unsupported target")
	}
	o, err := NewOFile("native.bo", "native")
	if err != nil {
		t.Fatal(err)
	}
	o.ExeFormat = string(DarwinARM64)
	o.NativeObject = []byte{0xcf, 0xfa, 0xed, 0xfe, 1, 2, 3}
	var b bytes.Buffer
	if err := writeOFile(&b, o); err != nil {
		t.Fatal(err)
	}
	got, err := readOFile(&b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.NativeObject, o.NativeObject) {
		t.Fatal("native payload changed")
	}
	if err := ValidateTarget(LinuxAMD64, []*OFile{got}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mixed target check: %v", err)
	}
	if err := ValidateTarget(DarwinARM64, []*OFile{got}); err != nil {
		t.Fatal(err)
	}
	// Empty ExeFormat is the legacy Linux target, and its wire layout stays unchanged.
	legacy, _ := NewOFile("old.bo", "old")
	b.Reset()
	if err := writeOFile(&b, legacy); err != nil {
		t.Fatal(err)
	}
	got, err = readOFile(&b)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTarget(LinuxAMD64, []*OFile{got}); err != nil {
		t.Fatal(err)
	}
}
