package macho

import (
	"bytes"
	"debug/macho"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// AssembleARM64 produces a relocatable Mach-O object using Apple's integrated
// assembler. Boson's metaassembler supplies the fully lowered native source.
func AssembleARM64(source string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "boson-arm64-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "package.o")
	cmd := exec.Command("clang", "-target", "arm64-apple-macos11", "-x", "assembler", "-c", "-o", path, "-")
	cmd.Stdin = bytes.NewBufferString(source)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ARM64 assembler: %w\n%s", err, output)
	}
	return os.ReadFile(path)
}

// ValidateARM64Object checks the native payload before handing it to the linker.
func ValidateARM64Object(image []byte) error {
	f, err := macho.NewFile(bytes.NewReader(image))
	if err != nil {
		return fmt.Errorf("invalid Mach-O object: %w", err)
	}
	defer f.Close()
	if f.Cpu != macho.CpuArm64 || f.Type != macho.TypeObj {
		return fmt.Errorf("expected ARM64 relocatable Mach-O, got CPU %v type %v", f.Cpu, f.Type)
	}
	return nil
}

// LinkARM64 delegates final Mach-O layout, dyld metadata and ad-hoc signing to
// the platform linker. Native objects keep all their typed Mach-O relocations.
func LinkARM64(output string, objects [][]byte) error {
	for _, image := range objects {
		if err := ValidateARM64Object(image); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "boson-link-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	args := []string{"-target", "arm64-apple-macos11", "-Wl,-dead_strip", "-Wl,-e,__init.start", "-o", filepath.Join(dir, "program")}
	for i, image := range objects {
		path := filepath.Join(dir, fmt.Sprintf("%d.o", i))
		if err := os.WriteFile(path, image, 0600); err != nil {
			return err
		}
		args = append(args, path)
	}
	cmd := exec.Command("clang", args...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Mach-O linker: %w\n%s", err, msg)
	}
	image, err := os.ReadFile(filepath.Join(dir, "program"))
	if err != nil {
		return err
	}
	return os.WriteFile(output, image, 0755)
}
