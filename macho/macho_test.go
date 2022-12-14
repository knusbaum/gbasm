package macho

import "testing"

func TestMacho(t *testing.T) {
	// 100003fa8:	48 c7 c7 0a 00 00 00	movq	$10, %rdi
	// 100003faf:	48 c7 c0 01 00 00 02	movq	$33554433, %rax
	// 100003fb6:	0f 05	syscall
	text := []byte{
		0x48, 0xc7, 0xc7, 0x0a, 0x00, 0x00, 0x00,
		0x48, 0xc7, 0xc0, 0x01, 0x00, 0x00, 0x02,
		0x0f, 0x05,
	}
	WriteMacho("/tmp/gbout", text)
}
