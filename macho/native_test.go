package macho

import (
	"bytes"
	stdmacho "debug/macho"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeARM64ObjectAndExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("requires Apple Silicon and Command Line Tools")
	}
	image, err := AssembleARM64(".text\n.globl __init.start\n__init.start:\nmov w0, #42\nret\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateARM64Object(image); err != nil {
		t.Fatal(err)
	}
	obj, err := stdmacho.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	code, err := obj.Section("__text").Data()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x40, 0x05, 0x80, 0x52, 0xc0, 0x03, 0x5f, 0xd6}
	if !bytes.Equal(code, want) {
		t.Fatalf("machine code %x, want %x", code, want)
	}
	exe := filepath.Join(t.TempDir(), "program")
	if err := LinkARM64(exe, [][]byte{image}); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("codesign", "--verify", exe).CombinedOutput(); err != nil {
		t.Fatalf("signature: %v: %s", err, output)
	}
	err = exec.Command(exe).Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 42 {
		t.Fatalf("native status: %v", err)
	}
}
func TestRejectInvalidNativeObject(t *testing.T) {
	if err := ValidateARM64Object([]byte("not Mach-O")); err == nil {
		t.Fatal("accepted invalid payload")
	}
}
