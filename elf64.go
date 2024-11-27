package gbasm

import (
	"bytes"
	"encoding/binary"
	"log"
	"os"
)

// https://www.uclibc.org/docs/elf-64-gen.pdf
type Elf64_Addr uint64
type Elf64_Off uint64
type Elf64_Half uint16
type Elf64_Word uint32
type Elf64_Sword int32
type Elf64_Xword uint64
type Elf64_Sxword int64

// e_type
const (
	ET_NONE   = 0      // No file type
	ET_REL    = 1      // Relocatable object file
	ET_EXEC   = 2      // Executable file
	ET_DYN    = 3      // Shared object file
	ET_CORE   = 4      // Core file
	ET_LOOS   = 0xFE00 //  Environment-specific use
	ET_HIOS   = 0xFEFF
	ET_LOPROC = 0xFF00 // Processor-specific use
	ET_HIPROC = 0xFFFF
)

// e_machine
const (
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
)

const (
	BITSIZE_32 = 1
	BITSIZE_64 = 2

	ENDIAN_LITTLE = 1
	ENDIAN_BIG    = 2

	// elf header version
	EV_NONE    = 0
	EV_CURRENT = 1

	ABI_SYSV = 0
)

type Elf64_Ehdr_Ident struct {
	magic    [4]byte // 4
	bitsize  byte    // 5
	endian   byte    // 6
	hVersion byte    // 7
	abi      byte    // 8
	padding  uint64  // 16
}

func makeHeaderIdent() Elf64_Ehdr_Ident {
	return Elf64_Ehdr_Ident{
		magic:    [4]byte{0x7F, 'E', 'L', 'F'},
		bitsize:  BITSIZE_64,
		endian:   ENDIAN_LITTLE,
		hVersion: EV_CURRENT,
		abi:      ABI_SYSV,
		// padding
	}
}

const Elf64_EhdrSize = 64

type Elf64_Ehdr struct {
	//e_ident     [16]byte   // ELF identification
	e_ident     Elf64_Ehdr_Ident
	e_type      Elf64_Half // Object file type
	e_machine   Elf64_Half // Machine type
	e_version   Elf64_Word // Object file version
	e_entry     Elf64_Addr // Entry point address
	e_phoff     Elf64_Off  // Program header offset
	e_shoff     Elf64_Off  // Section header offset
	e_flags     Elf64_Word // Processor-specific flags
	e_ehsize    Elf64_Half // ELF header size
	e_phentsize Elf64_Half // Size of program header entry
	e_phnum     Elf64_Half // Number of program header entries
	e_shentsize Elf64_Half // Size of section header entry
	e_shnum     Elf64_Half // Number of section header entries
	e_shstrndx  Elf64_Half // Section name string table index
}

// sh_type
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

// sh_link
// SHT_DYNAMIC String table used by entries in this section
// SHT_HASH Symbol table to which the hash table applies
//
// SHT_REL Symbol table referenced by relocations
// SHT_RELA
//
// SHT_SYMTAB String table used by entries in this section
// SHT_DYNSYM
//
// Other SHN_UNDEF

// sh_info
// SHT_REL Section index of section to which the relocations apply
// SHT_RELA
//
// SHT_SYMTAB Index of first non-local symbol (i.e., number of local symbols)
// SHT_DYNSYM
//
// Other 0

// Special section header indexes
const (
	SHN_UNDEF  = 0      // Used to mark an undefined or meaningless section reference
	SHN_LOPROC = 0xFF00 // Processor-specific use
	SHN_HIPROC = 0xFF1F
	SHN_LOOS   = 0xFF20 // Environment-specific use
	SHN_HIOS   = 0xFF3F
	SHN_ABS    = 0xFFF1 // Indicates that the corresponding reference is an absolute value
	SHN_COMMON = 0xFFF2 // Indicates a symbol that has been declared as a common block (Fortran COMMON or C tentative declaration)
)

const Elf64_ShdrSize = 64

type Elf64_Shdr struct {
	sh_name      Elf64_Word  // Section name
	sh_type      Elf64_Word  // Section type
	sh_flags     Elf64_Xword // Section attributes
	sh_addr      Elf64_Addr  // Virtual address in memory
	sh_offset    Elf64_Off   // Offset in file
	sh_size      Elf64_Xword // Size of section
	sh_link      Elf64_Word  // Link to other section
	sh_info      Elf64_Word  // Miscellaneous information
	sh_addralign Elf64_Xword // Address alignment boundary
	sh_entsize   Elf64_Xword // Size of entries, if section has table
}

// st_info high bits
const (
	STB_LOCAL  = 0  // Not visible outside the object file
	STB_GLOBAL = 1  // Global symbol, visible to all object files
	STB_WEAK   = 2  // Global scope, but with lower precedence than global symbols
	STB_LOOS   = 10 // Environment-specific use
	STB_HIOS   = 12
	STB_LOPROC = 13 // Processor-specific use
	STB_HIPROC = 15
)

// st_info low bits
const (
	STT_NOTYPE  = 0  // No type specified (e.g., an absolute symbol)
	STT_OBJECT  = 1  // Data object
	STT_FUNC    = 2  // Function entry point
	STT_SECTION = 3  // Symbol is associated with a section
	STT_FILE    = 4  // Source file associated with the object file
	STT_LOOS    = 10 // Environment-specific use
	STT_HIOS    = 12
	STT_LOPROC  = 13 // Processor-specific use
	STT_HIPROC  = 15
)

const Elf64_SymSize = 24

type Elf64_Sym struct {
	st_name  Elf64_Word  // Symbol name
	st_info  byte        // Type and Binding attributes. binding is high 4, type is low 4
	st_other byte        // Reserved
	st_shndx Elf64_Half  // Section table index
	st_value Elf64_Addr  // Symbol value
	st_size  Elf64_Xword // Size of object (e.g., common)
}

// p_type
const (
	PT_NULL    = 0          // Unused entry
	PT_LOAD    = 1          // Loadable segment
	PT_DYNAMIC = 2          // Dynamic linking tables
	PT_INTERP  = 3          // Program interpreter path name
	PT_NOTE    = 4          // Note sections
	PT_SHLIB   = 5          // Reserved
	PT_PHDR    = 6          // Program header table
	PT_LOOS    = 0x60000000 // Environment-specific use
	PT_HIOS    = 0x6FFFFFFF
	PT_LOPROC  = 0x70000000 // Processor-specific use
	PT_HIPROC  = 0x7FFFFFFF //
)

// p_flags
const (
	PF_X        = 0x1        // Execute permission
	PF_W        = 0x2        // Write permission
	PF_R        = 0x4        // Read permission
	PF_MASKOS   = 0x00FF0000 // These flag bits are reserved for environment-specific use
	PF_MASKPROC = 0xFF000000 // These flag bits are reserved for processor-specific use
)

const Elf64_PhdrSize = 56

type Elf64_Phdr struct {
	p_type   Elf64_Word  // Type of segment
	p_flags  Elf64_Word  // Segment attributes
	p_offset Elf64_Off   // Offset in file
	p_vaddr  Elf64_Addr  // Virtual address in memory
	p_paddr  Elf64_Addr  // Reserved
	p_filesz Elf64_Xword // Size of segment in file
	p_memsz  Elf64_Xword // Size of segment in memory
	p_align  Elf64_Xword // Alignment of segment
}

type Elf64_Symbol struct {
	Name    string
	Type    int
	Address uint64 // This should be the final virtual address for the object
	Size    int
}

type Elf64_Section struct {
	//header Elf64_Shdr
	name     string
	s_type   Elf64_Word
	flags    Elf64_Xword
	addr     Elf64_Addr
	data     []byte
	loadable bool
	syms     []Elf64_Symbol
}

type strtab struct {
	m  map[string]uint32
	bs bytes.Buffer
}

func newstrtab() strtab {
	s := strtab{
		m: make(map[string]uint32),
	}
	s.bs.WriteByte(0)
	return s
}

func (t *strtab) StrOff(s string) Elf64_Word {
	if o, ok := t.m[s]; ok {
		return Elf64_Word(o)
	}
	off := t.bs.Len()
	//t.bs.WriteByte(0)
	t.bs.WriteString(s)
	t.bs.WriteByte(0)
	t.m[s] = uint32(off)
	return Elf64_Word(off)
}

const ENTRY_ADDR = 0x30000

func WriteElf(exename string, sections []Elf64_Section) {

	var nphdrs int
	for _, sect := range sections {
		if sect.loadable {
			nphdrs++
		}
	}

	// Needs:
	// e_phnum
	// e_shoff
	elfHdr := Elf64_Ehdr{
		e_ident:     makeHeaderIdent(),
		e_type:      ET_EXEC,
		e_machine:   EM_AMD64,
		e_version:   EV_CURRENT,
		e_entry:     ENTRY_ADDR,
		e_phoff:     Elf64_EhdrSize,
		e_shoff:     Elf64_EhdrSize + (Elf64_PhdrSize * Elf64_Off(nphdrs)),
		e_ehsize:    Elf64_EhdrSize,
		e_phentsize: Elf64_PhdrSize,
		e_phnum:     Elf64_Half(nphdrs),
		e_shentsize: Elf64_ShdrSize,
		e_shnum:     Elf64_Half(len(sections) + 4), // Number of sections plus null, symtab, shstrtab, strtab
		e_shstrndx:  Elf64_Half(len(sections) + 3), // shstrndx is the last section
	}

	dataOff := Elf64_Off(Elf64_EhdrSize+(Elf64_PhdrSize*Elf64_Off(nphdrs))+(Elf64_ShdrSize*Elf64_Off(elfHdr.e_shnum))+0x01000) & (^Elf64_Off(0xFFF))

	var shdrs []Elf64_Shdr
	var phdrs []Elf64_Phdr
	var symbs bytes.Buffer
	binary.Write(&symbs, binary.LittleEndian, Elf64_Sym{})
	symcount := 1

	shst := newstrtab()
	st := newstrtab()
	for secti, sect := range sections {
		sHdr := Elf64_Shdr{
			sh_name:   shst.StrOff(sect.name),
			sh_type:   sect.s_type,
			sh_flags:  sect.flags,
			sh_addr:   sect.addr,
			sh_offset: dataOff,
			sh_size:   Elf64_Xword(len(sect.data)),
			//sh_addralign: 0x1000,
			sh_addralign: 0x8,
		}
		if sect.loadable {
			pHdr := Elf64_Phdr{
				p_type:   PT_LOAD,
				p_flags:  PF_R, // | PF_W, //PF_X | PF_R,
				p_align:  0x8,  //0x1000,
				p_offset: Elf64_Off(dataOff),
				p_vaddr:  Elf64_Addr(sect.addr),
				p_paddr:  Elf64_Addr(sect.addr), // needed?
				p_filesz: Elf64_Xword(len(sect.data)),
				p_memsz:  Elf64_Xword(len(sect.data)),
			}

			if sect.flags&SHF_EXECINSTR != 0 {
				pHdr.p_flags |= PF_X
				pHdr.p_align = 0x1000
				sHdr.sh_addralign = 0x1000
			} else if sect.flags&SHF_WRITE != 0 {
				pHdr.p_flags |= PF_W
			}
			phdrs = append(phdrs, pHdr)

			for _, sym := range sect.syms {
				var info byte
				if sym.Type == SYM_FUNC {
					info = STT_FUNC
				} else if sym.Type == SYM_OBJECT {
					info = STT_OBJECT
				}
				//fmt.Printf("SYMBOL %s IN SECTION %d AT OFFSET: %v\n", sym.Name, secti, sym.Address)
				binary.Write(&symbs, binary.LittleEndian, Elf64_Sym{
					st_name:  st.StrOff(sym.Name),
					st_info:  info, //| (STB_GLOBAL << 4),
					st_shndx: Elf64_Half(secti + 1),
					st_value: Elf64_Addr(sym.Address),
					st_size:  Elf64_Xword(sym.Size),
				})
				symcount++
			}
		}
		dataOff += Elf64_Off(len(sect.data)+0x1000) & (^Elf64_Off(0xFFF))
		shdrs = append(shdrs, sHdr)
	}

	symSect := Elf64_Shdr{
		sh_name: shst.StrOff(".symtab"),
		sh_type: SHT_SYMTAB,
		//sh_flags:
		//sh_addr:
		sh_offset:    dataOff,
		sh_size:      Elf64_Xword(symbs.Len()),
		sh_link:      Elf64_Word(elfHdr.e_shstrndx - 1),
		sh_info:      Elf64_Word(symcount), //0x1,
		sh_addralign: 0x8,
		sh_entsize:   Elf64_SymSize,
	}
	dataOff += Elf64_Off(symbs.Len()+0x1000) & (^Elf64_Off(0xFFF))
	strSect := Elf64_Shdr{
		sh_name:      shst.StrOff(".strtab"),
		sh_type:      SHT_STRTAB,
		sh_offset:    dataOff,
		sh_size:      Elf64_Xword(st.bs.Len()),
		sh_addralign: 0x1,
	}
	dataOff += Elf64_Off(st.bs.Len()+0x1000) & (^Elf64_Off(0xFFF))
	shStrSect := Elf64_Shdr{
		sh_name:      shst.StrOff(".shstrtab"),
		sh_type:      SHT_STRTAB,
		sh_offset:    dataOff,
		sh_size:      Elf64_Xword(shst.bs.Len()),
		sh_addralign: 0x1,
	}

	// Write the things.
	f, err := os.OpenFile(exename, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0755)
	if err != nil {
		log.Fatalf("Failed to create file: %s", err)
	}
	defer f.Close()

	// Write the ELF Header
	binary.Write(f, binary.LittleEndian, elfHdr)

	// Write all the Program Headers
	for _, h := range phdrs {
		binary.Write(f, binary.LittleEndian, h)
	}

	// Write the first (NULL) section
	binary.Write(f, binary.LittleEndian, Elf64_Shdr{})

	// Write all the user-provided sections
	for _, sect := range shdrs {
		binary.Write(f, binary.LittleEndian, sect)
	}

	binary.Write(f, binary.LittleEndian, symSect)
	binary.Write(f, binary.LittleEndian, strSect)
	binary.Write(f, binary.LittleEndian, shStrSect)

	for i, sect := range sections {
		// 		current_off, err := f.Seek(0, 1)
		// 		if err != nil {
		// 			panic(err)
		// 		}
		//
		// 		target_off := int64(shdrs[i].sh_offset)
		// 		fmt.Printf("For section %s, current offset: %d (%X), target offset: %d (%X)\n", sect.name, current_off, current_off, target_off, target_off)
		// 		bs := make([]byte, target_off-current_off)
		// 		f.Write(bs)
		// 		f.Write(sect.data)
		WriteSection(f, sect.data, int64(shdrs[i].sh_offset))
	}

	// 	current_off, err := f.Seek(0, 1)
	// 	if err != nil {
	// 		panic(err)
	// 	}
	//
	// 	target_off := int64(shStrSect.sh_offset)
	// 	fmt.Printf("For section %s, current offset: %d (%X), target offset: %d (%X)\n", "STRINGS", current_off, current_off, target_off, target_off)
	// 	bs := make([]byte, target_off-current_off)
	// 	f.Write(bs)
	// 	f.Write(shst.bs.Bytes())

	WriteSection(f, symbs.Bytes(), int64(symSect.sh_offset))
	WriteSection(f, st.bs.Bytes(), int64(strSect.sh_offset))
	WriteSection(f, shst.bs.Bytes(), int64(shStrSect.sh_offset))

}

func WriteSection(f *os.File, data []byte, target_off int64) {
	current_off, err := f.Seek(0, 1)
	if err != nil {
		panic(err)
	}

	//target_off := int64(shStrSect.sh_offset)
	//fmt.Printf("For section %s, current offset: %d (%X), target offset: %d (%X)\n", "STRINGS", current_off, current_off, target_off, target_off)
	bs := make([]byte, target_off-current_off)
	f.Write(bs)
	//f.Write(shst.bs.Bytes())
	f.Write(data)
}
