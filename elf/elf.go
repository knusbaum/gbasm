package elf

import (
	"encoding/binary"
	"io"
	"log"
	"os"
)

const header64Size = 64

// A header is the first section of an elf file.
// Source: https://wiki.osdev.org/ELF
//  Header
//
// The header is found at the start of the ELF file.
// Position (32 bit) 	Position (64 bit) 	Value
// 0-3 	0-3 	Magic number - 0x7F, then 'ELF' in ASCII
// 4 	4 	1 = 32 bit, 2 = 64 bit
// 5 	5 	1 = little endian, 2 = big endian
// 6 	6 	ELF header version
// 7 	7 	OS ABI - usually 0 for System V
// 8-15 	8-15 	Unused/padding
// 16-17 	16-17 	1 = relocatable, 2 = executable, 3 = shared, 4 = core
// 18-19 	18-19 	Instruction set - see table below
// 20-23 	20-23 	ELF Version
// 24-27 	24-31 	Program entry position
// 28-31 	32-39 	Program header table position
// 32-35 	40-47 	Section header table position
// 36-39 	48-51 	Flags - architecture dependent; see note below
// 40-41 	52-53 	Header size
// 42-43 	54-55 	Size of an entry in the program header table
// 44-45 	56-57 	Number of entries in the program header table
// 46-47 	58-59 	Size of an entry in the section header table
// 48-49 	60-61 	Number of entries in the section header table
// 50-51 	62-63 	Index in section header table with the section names
type header64 struct {
	magic       [4]byte // 4
	bitsize     uint8   // 5
	endian      uint8   // 6
	hVersion    uint8   // 7
	abi         uint8   // 8
	padding     uint64  // 16
	elfType     uint16  // 18
	machine     uint16  // 20
	elfVersion  uint32  // 22
	entryAddr   uint64  // 30
	peOff       uint64  // 38
	shOff       uint64  // 46
	flags       uint32  // 50
	hSize       uint16  // 52
	phEntrySize uint16  // 54
	phCount     uint16  // 56
	shEntrySize uint16  // 58
	shCount     uint16  // 60
	shStrIndex  uint16  // 62
}

func (h *header64) Write(w io.Writer) {
	// TODO: don't ignore all errors.
	binary.Write(w, binary.LittleEndian, h)
}

const (
	BITSIZE_32 = 1
	BITSIZE_64 = 2

	ENDIAN_LITTLE = 1
	ENDIAN_BIG    = 2

	// elf header version
	EV_NONE    = 0
	EV_CURRENT = 1

	ABI_SYSV = 0

	ET_NONE   = 0
	ET_REL    = 1      // Relocatable
	ET_EXEC   = 2      // Executable
	ET_DYN    = 3      // Shared Object
	ET_CORE   = 4      // Core File
	ET_LOPROC = 0xff00 // Processor-specific
	ET_HIPROC = 0xffff // Processor-specific

	EM_NONE    = 0
	EM_SPARC   = 2
	EM_386     = 3
	EM_MIPS    = 8
	EM_PPC     = 0x14
	EM_ARM     = 0x28
	EM_SUPERH  = 0x2A
	EM_IA64    = 0x32
	EM_AMD64   = 0x3E
	EM_AARCH64 = 0xB7
	EM_RISCV   = 0xF3

	ENTRY_ADDR = 0x30000

	SHN_UNDEF = 0
)

var amd64LinuxHeader = header64{
	magic:    [4]byte{0x7F, 'E', 'L', 'F'},
	bitsize:  BITSIZE_64,
	endian:   ENDIAN_LITTLE,
	hVersion: EV_CURRENT,
	abi:      ABI_SYSV,
	// padding
	elfType:     ET_EXEC, // We're only writing executable files.
	machine:     EM_AMD64,
	elfVersion:  EV_CURRENT,
	entryAddr:   ENTRY_ADDR,
	peOff:       header64Size,
	shOff:       0,
	flags:       0,
	hSize:       header64Size,
	phEntrySize: programHeader64Size,
	phCount:     1,
	shEntrySize: 0,
	shCount:     0,
	shStrIndex:  SHN_UNDEF,
}

const programHeader64Size = 56

// Program Headers:
// Segment types:
// 0 = null - ignore the entry;
// 1 = load - clear p_memsz bytes at p_vaddr to 0, then copy p_filesz bytes from p_offset to p_vaddr;
// 2 = dynamic - requires dynamic linking;
// 3 = interp - contains a file path to an executable to use as an interpreter for the following segment;
// 4 = note section. There are more values, but mostly contain architecture/environment specific
//     information, which is probably not required for the majority of ELF files.
//
// Flags:
// 1 = executable,
// 2 = writable,
// 4 = readable.

const (
	SEGTYPE_NULL    = 0
	SEGTYPE_LOAD    = 1
	SEGTYPE_DYNAMIC = 2
	SEGTYPE_INTERP  = 3
	SEGTYPE_NOTE    = 4

	PHFLAG_EXE   = 1
	PHFLAG_WRITE = 2
	PHFLAG_READ  = 4
)

type programHeader64 struct {
	segType  uint32
	flags    uint32
	p_offset uint64
	p_vaddr  uint64
	p_paddr  uint64
	p_filesz uint64
	p_memsz  uint64
	align    uint64
}

func (h *programHeader64) Write(w io.Writer) {
	// TODO: don't ignore all errors.
	binary.Write(w, binary.LittleEndian, h)
}

func makeProgramHeader64(text []byte) programHeader64 {
	return programHeader64{
		segType:  SEGTYPE_LOAD,
		flags:    PHFLAG_EXE | PHFLAG_READ,
		p_offset: (header64Size + programHeader64Size + 0x1000) & 0xFFFFFFFFFFFFFFFFFFFFF000,
		p_vaddr:  ENTRY_ADDR,
		p_paddr:  ENTRY_ADDR,
		p_filesz: uint64(len(text)),
		p_memsz:  uint64(len(text)),
		align:    0x1000,
	}
}

// Name Size Alignment Purpose
// Elf64_Addr 8 8 Unsigned program address
// Elf64_Off 8 8 Unsigned file offset
// Elf64_Half 2 2 Unsigned medium integer
// Elf64_Word 4 4 Unsigned integer
// Elf64_Sword 4 4 Signed integer
// Elf64_Xword 8 8 Unsigned long integer
// Elf64_Sxword 8 8 Signed long integer
// unsigned char 1 1 Unsigned small integer
// typedef struct
// {
// 	Elf64_Word sh_name; /* Section name */
// 	Elf64_Word sh_type; /* Section type */
// 	Elf64_Xword sh_flags; /* Section attributes */
// 	Elf64_Addr sh_addr; /* Virtual address in memory */
// 	Elf64_Off sh_offset; /* Offset in file */
// 	Elf64_Xword sh_size; /* Size of section */
// 	Elf64_Word sh_link; /* Link to other section */
// 	Elf64_Word sh_info; /* Miscellaneous information */
// 	Elf64_Xword sh_addralign; /* Address alignment boundary */
// 	Elf64_Xword sh_entsize; /* Size of entries, if section has table */
// } Elf64_Shdr;
// type sectionHeader64 struct {
// 	sh_name uint32
// 	sh_
// }

func WriteELF(exename string, text []byte) error {
	f, err := os.Create(exename)
	if err != nil {
		log.Fatalf("Failed to create file: %s", err)
	}
	defer f.Close()
	amd64LinuxHeader.Write(f)
	ph := makeProgramHeader64(text)
	ph.Write(f)
	current_off := uint64(header64Size + programHeader64Size)
	target_off := ph.p_offset
	bs := make([]byte, target_off-current_off)
	f.Write(bs)
	f.Write(text)
	return nil
}
