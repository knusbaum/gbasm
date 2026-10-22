package gbasm

import "fmt"

// Target identifies the operating system and instruction set of an object.
// The empty spelling preserves the historical Linux/AMD64 default.
type Target string

const (
	LinuxAMD64  Target = "linux/amd64"
	DarwinARM64 Target = "darwin/arm64"
)

func ParseTarget(s string) (Target, error) {
	if s == "" {
		return LinuxAMD64, nil
	}
	t := Target(s)
	switch t {
	case LinuxAMD64, DarwinARM64:
		return t, nil
	}
	return "", fmt.Errorf("unsupported target %q (want linux/amd64 or darwin/arm64)", s)
}

func (o *OFile) Target() (Target, error) { return ParseTarget(o.ExeFormat) }

// ValidateTarget rejects incompatible inputs before any linking or output writes.
func ValidateTarget(t Target, objects []*OFile) error {
	normalized, err := ParseTarget(string(t))
	if err != nil {
		return err
	}
	t = normalized
	for _, o := range objects {
		ot, err := o.Target()
		if err != nil {
			return fmt.Errorf("%s: %w", o.Filename, err)
		}
		if ot != t {
			return fmt.Errorf("%s: target %s does not match link target %s", o.Filename, ot, t)
		}
	}
	return nil
}
