package elf

import (
	"encoding/binary"
	"io"
	"log"
	"os"
)

const header64Size = 64

// See also: https://uclibc.org/docs/elf-64-gen.pdf

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
	peOff       uint64  // 38	Program Header table offset
	shOff       uint64  // 46	Secction Header table offset
	flags       uint32  // 50
	ehSize      uint16  // 52	Size in bytes of the ELF header
	phEntrySize uint16  // 54	Size in bytes of a program header table entry
	phNum       uint16  // 56	Number of entries in the program header table
	shEntrySize uint16  // 58	Size in bytes of a section header table entry
	shNum       uint16  // 60	Number of entries in the section header table
	shStrIndex  uint16  // 62	The section header table index of the section containing the section name string table
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
	shOff:       header64Size + programHeader64Size,
	flags:       0,
	ehSize:      header64Size,
	phEntrySize: programHeader64Size,
	phNum:       1,
	shEntrySize: sectionHeader64Size,
	shNum:       2,
	shStrIndex:  1,
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
		p_offset: (header64Size + programHeader64Size + (sectionHeader64Size * 2) + 0x1000) & 0xFFFFFFFFFFFFFFFFFFFFF000,
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

const sectionHeader64Size = 64

type sectionHeader64 struct {
	sh_name      uint32 //	The offset in bytes to the section name, relative to the start of the section name string table
	sh_type      uint32 //	The section type (enum)
	sh_flags     uint64 //
	sh_addr      uint64 //	The virtual address of the beginning of the section in memory
	sh_offset    uint64 //	The offset, in bytes, of the beginning of the section contents in the file
	sh_size      uint64 //	The size, in bytes, of the section in the file (unless the type is SHT_NOBITS)
	sh_link      uint32 //	The section index of an associated section
	sh_info      uint32 //	Extra info about the section
	sh_addralign uint64 //	Required alignment of the section (must be power of 2)
	sh_entsize   uint64 //	The size in bytes of each entry for sections that contain fixed-size entries, otherwise zero.
}

// These are the Section Types for use in the sh_type field of the sectionHeader64
const (
	SHT_NULL     = 0          // Marks an unused section header
	SHT_PROGBITS = 1          // Contains information defined by the program
	SHT_SYMTAB   = 2          // Contains a linker symbol table
	SHT_STRTAB   = 3          // Contains a string table
	SHT_RELA     = 4          // Contains “Rela” type relocation entries
	SHT_HASH     = 5          // Contains a symbol hash table
	SHT_DYNAMIC  = 6          // Contains dynamic linking tables
	SHT_NOTE     = 7          // Contains note information
	SHT_NOBITS   = 8          // Contains uninitialized space; does not occupy any space in the file
	SHT_REL      = 9          // Contains “Rel” type relocation entries
	SHT_SHLIB    = 10         // Reserved
	SHT_DYNSYM   = 11         // Contains a dynamic loader symbol table
	SHT_LOOS     = 0x60000000 // Environment-specific use
	SHT_HIOS     = 0x6FFFFFFF
	SHT_LOPROC   = 0x70000000 // Processor-specific use
	SHT_HIPROC   = 0x7FFFFFFF
)

// sh_flags
const (
	SHF_WRITE     = 0x1        // Section contains writable data
	SHF_ALLOC     = 0x2        // Section is allocated in memory image of program
	SHF_EXECINSTR = 0x4        // Section contains executable instructions
	SHF_MASKOS    = 0x0F000000 // Environment-specific use
	SHF_MASKPROC  = 0xF0000000 // Processor-specific use
)

func WriteELF(exename string, text []byte) error {
	f, err := os.Create(exename)
	if err != nil {
		log.Fatalf("Failed to create file: %s", err)
	}
	defer f.Close()
	amd64LinuxHeader.Write(f)
	ph := makeProgramHeader64(text)
	ph.Write(f)
	textSection := sectionHeader64{
		sh_name:      1,
		sh_type:      SHT_PROGBITS,
		sh_addr:      ENTRY_ADDR,
		sh_offset:    ph.p_offset,
		sh_size:      uint64(len(text)),
		sh_addralign: 0x1000,
	}
	sectionStringsTXT := []byte("\x00.text\x00.shstrtab\x00")
	sectionStrings := sectionHeader64{
		sh_name:   7,
		sh_type:   SHT_STRTAB,
		sh_offset: ph.p_offset + uint64(len(text)),
		sh_size:   uint64(len(sectionStringsTXT)),
	}
	binary.Write(f, binary.LittleEndian, textSection)
	binary.Write(f, binary.LittleEndian, sectionStrings)
	//	bs1 := make([]byte, sectionHeader64Size)
	//	f.Write(bs1)
	current_off := uint64(header64Size + programHeader64Size + (sectionHeader64Size * 2))
	target_off := ph.p_offset
	bs := make([]byte, target_off-current_off)
	f.Write(bs)
	f.Write(text)
	f.Write(sectionStringsTXT)
	return nil
}
