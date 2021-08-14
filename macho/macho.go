package macho

import (
	"encoding/binary"
	"io"
	"log"
	"os"

	"github.com/Binject/debug/macho"
)

// Source: https://www.symbolcrash.com/wp-content/uploads/2019/02/MachORuntime.pdf
// Source: https://github.com/Homebrew/ruby-macho/blob/master/lib/macho/headers.rb
const (
	CPU_ARCH_ABI64         = 0x01000000
	CPU_TYPE_I386          = 0x00000007
	CPU_TYPE_X86_64        = CPU_ARCH_ABI64 | CPU_TYPE_I386
	CPU_SUBTYPE_LIB64      = 0x80000000
	CPU_SUBTYPE_I386       = 0x00000003
	CPU_SUBTYPE_X86_64_ALL = CPU_SUBTYPE_I386
	MH_EXECUTE             = 0x2
	MH_NOUNDEFS            = 0x1
	MH_PIE                 = 0x200000

	// From: /Library/Developer/CommandLineTools/SDKs/MacOSX10.14.sdk/usr/include/mach-o/loader.h
	SECTION_ATTRIBUTES_USR   = 0xff000000 /* User setable attributes */
	S_ATTR_PURE_INSTRUCTIONS = 0x80000000 /* section contains only true
	   machine instructions */
	S_ATTR_NO_TOC = 0x40000000 /* section contains coalesced
	   symbols that are not to be
	   in a ranlib table of
	   contents */
	S_ATTR_STRIP_STATIC_SYMS = 0x20000000 /* ok to strip static symbols
	   in this section in files
	   with the MH_DYLDLINK flag */
	S_ATTR_NO_DEAD_STRIP = 0x10000000 /* no dead stripping */
	S_ATTR_LIVE_SUPPORT  = 0x08000000 /* blocks are live if they
	   reference live blocks */
	S_ATTR_SELF_MODIFYING_CODE = 0x04000000 /* Used with i386 code stubs
	   written on by dyld */
	/*
	 * If a segment contains any sections marked with S_ATTR_DEBUG then all
	 * sections in that segment must have this attribute.  No section other than
	 * a section marked with this attribute may reference the contents of this
	 * section.  A section with this attribute may contain no symbols and must have
	 * a section type S_REGULAR.  The static linker will not copy section contents
	 * from sections with this attribute into its output file.  These sections
	 * generally contain DWARF debugging info.
	 */
	S_ATTR_DEBUG             = 0x02000000 /* a debug section */
	SECTION_ATTRIBUTES_SYS   = 0x00ffff00 /* system setable attributes */
	S_ATTR_SOME_INSTRUCTIONS = 0x00000400 /* section contains some
	   machine instructions */
	S_ATTR_EXT_RELOC = 0x00000200 /* section has external
	   relocation entries */
	S_ATTR_LOC_RELOC = 0x00000100 /* section has local
	   relocation entries */

)

type vm_prot_t uint32

const (
	VM_PROT_READ    vm_prot_t = 0x1
	VM_PROT_WRITE   vm_prot_t = 0x2
	VM_PROT_EXECUTE vm_prot_t = 0x4
)

// ; Mach-O header
// DD		MH_MAGIC_64										; magic
// DD		CPU_TYPE_X86_64									; cputype
// DD		CPU_SUBTYPE_LIB64 | CPU_SUBTYPE_I386_ALL		; cpusubtype
// DD		MH_EXECUTE										; filetype
// DD		2												; ncmds
// DD		end - load_commands								; sizeofcmds
// DD		MH_NOUNDEFS										; flags
// DD		0x0												; reserved

// From: /Library/Developer/CommandLineTools/SDKs/MacOSX10.14.sdk/usr/include/mach-o/loader.h
/* Constant for the magic field of the mach_header_64 (64-bit architectures) */
const (
	MH_MAGIC_64 = 0xfeedfacf /* the 64-bit mach magic number */
)

const machHeaderSize = 32

type machHeader struct {
	magic      uint32
	cputype    uint32
	cpusubtype uint32
	filetype   uint32
	ncmds      uint32
	sizeofcmds uint32
	flags      uint32
	reserved   uint32
}

func (h *machHeader) Write(w io.Writer) {
	binary.Write(w, binary.LittleEndian, h.magic)
	binary.Write(w, binary.LittleEndian, h.cputype)
	binary.Write(w, binary.LittleEndian, h.cpusubtype)
	binary.Write(w, binary.LittleEndian, h.filetype)
	binary.Write(w, binary.LittleEndian, h.ncmds)
	binary.Write(w, binary.LittleEndian, h.sizeofcmds)
	binary.Write(w, binary.LittleEndian, h.flags)
	binary.Write(w, binary.LittleEndian, h.reserved)
}

const loadCmdSegmentSize = 72

// From: /Library/Developer/CommandLineTools/SDKs/MacOSX10.14.sdk/usr/include/mach-o/loader.h:/^struct segment_command_64
type loadCmdSegment struct {
	cmd      uint32
	cmdsize  uint32
	segname  string // [16]byte
	vmaddr   uint64
	vmsize   uint64
	fileoff  uint64
	filesize uint64
	maxprot  vm_prot_t
	initprot vm_prot_t
	nsects   uint32
	flags    uint32
}

func (s *loadCmdSegment) Write(w io.Writer) {
	binary.Write(w, binary.LittleEndian, s.cmd)
	binary.Write(w, binary.LittleEndian, s.cmdsize)
	var segname [16]byte
	sbs := segname[:16]
	copy(sbs, []byte(s.segname))
	binary.Write(w, binary.LittleEndian, sbs)
	binary.Write(w, binary.LittleEndian, s.vmaddr)
	binary.Write(w, binary.LittleEndian, s.vmsize)
	binary.Write(w, binary.LittleEndian, s.fileoff)
	binary.Write(w, binary.LittleEndian, s.filesize)
	binary.Write(w, binary.LittleEndian, s.maxprot)
	binary.Write(w, binary.LittleEndian, s.initprot)
	binary.Write(w, binary.LittleEndian, s.nsects)
	binary.Write(w, binary.LittleEndian, s.flags)
}

const sectionSize = 80

// From: /Library/Developer/CommandLineTools/SDKs/MacOSX10.14.sdk/usr/include/mach-o/loader.h:/^struct section_64
type section struct {
	sectname  string // [16]byte
	segname   string // [16]byte
	addr      uint64
	size      uint64
	offset    uint32
	align     uint32
	reloff    uint32
	nreloc    uint32
	flags     uint32
	reserved1 uint32
	reserved2 uint32
	reserved3 uint32
}

func (s *section) Write(w io.Writer) {
	var str [16]byte
	sbs := str[:16]
	copy(sbs, []byte(s.sectname))
	binary.Write(w, binary.LittleEndian, sbs)
	copy(sbs, []byte(s.segname))
	binary.Write(w, binary.LittleEndian, sbs)
	binary.Write(w, binary.LittleEndian, s.addr)
	binary.Write(w, binary.LittleEndian, s.size)
	binary.Write(w, binary.LittleEndian, s.offset)
	binary.Write(w, binary.LittleEndian, s.align)
	binary.Write(w, binary.LittleEndian, s.reloff)
	binary.Write(w, binary.LittleEndian, s.nreloc)
	binary.Write(w, binary.LittleEndian, s.flags)
	binary.Write(w, binary.LittleEndian, s.reserved1)
	binary.Write(w, binary.LittleEndian, s.reserved2)
	binary.Write(w, binary.LittleEndian, s.reserved3)
}

// From: /Library/Developer/CommandLineTools/SDKs/MacOSX10.14.sdk/usr/include/mach/i386/_structs.h:652
// #define	_STRUCT_X86_THREAD_STATE64	struct x86_thread_state64
// _STRUCT_X86_THREAD_STATE64
// {
// 	__uint64_t	rax;
// 	__uint64_t	rbx;
// 	__uint64_t	rcx;
// 	__uint64_t	rdx;
// 	__uint64_t	rdi;
// 	__uint64_t	rsi;
// 	__uint64_t	rbp;
// 	__uint64_t	rsp;
// 	__uint64_t	r8;
// 	__uint64_t	r9;
// 	__uint64_t	r10;
// 	__uint64_t	r11;
// 	__uint64_t	r12;
// 	__uint64_t	r13;
// 	__uint64_t	r14;
// 	__uint64_t	r15;
// 	__uint64_t	rip;
// 	__uint64_t	rflags;
// 	__uint64_t	cs;
// 	__uint64_t	fs;
// 	__uint64_t	gs;
// };

const (
	x86_THREAD_STATE64 = 4
	LC_THREAD          = 0x4 /* thread */
	LC_UNIXTHREAD      = 0x5 /* unix thread (includes a stack) */
)
const threadStateDwords = 42

type thread_state64 struct {
	rax    uint64
	rbx    uint64
	rcx    uint64
	rdx    uint64
	rdi    uint64
	rsi    uint64
	rbp    uint64
	rsp    uint64
	r8     uint64
	r9     uint64
	r10    uint64
	r11    uint64
	r12    uint64
	r13    uint64
	r14    uint64
	r15    uint64
	rip    uint64
	rflags uint64
	cs     uint64
	fs     uint64
	gs     uint64
}

const threadCommandSize = 184

type threadCommand struct {
	cmd     uint32         /* LC_THREAD or  LC_UNIXTHREAD */
	cmdsize uint32         /* total size of this command */
	flavor  uint32         /* flavor of thread state */
	count   uint32         /* count of uint32_t's in thread state */
	state   thread_state64 /* thread state for this flavor */
}

func (c *threadCommand) Write(w io.Writer) {
	binary.Write(w, binary.LittleEndian, c.cmd)
	binary.Write(w, binary.LittleEndian, c.cmdsize)
	binary.Write(w, binary.LittleEndian, c.flavor)
	binary.Write(w, binary.LittleEndian, c.count)
	binary.Write(w, binary.LittleEndian, c.state.rax)
	binary.Write(w, binary.LittleEndian, c.state.rbx)
	binary.Write(w, binary.LittleEndian, c.state.rcx)
	binary.Write(w, binary.LittleEndian, c.state.rdx)
	binary.Write(w, binary.LittleEndian, c.state.rdi)
	binary.Write(w, binary.LittleEndian, c.state.rsi)
	binary.Write(w, binary.LittleEndian, c.state.rbp)
	binary.Write(w, binary.LittleEndian, c.state.rsp)
	binary.Write(w, binary.LittleEndian, c.state.r8)
	binary.Write(w, binary.LittleEndian, c.state.r9)
	binary.Write(w, binary.LittleEndian, c.state.r10)
	binary.Write(w, binary.LittleEndian, c.state.r11)
	binary.Write(w, binary.LittleEndian, c.state.r12)
	binary.Write(w, binary.LittleEndian, c.state.r13)
	binary.Write(w, binary.LittleEndian, c.state.r14)
	binary.Write(w, binary.LittleEndian, c.state.r15)
	binary.Write(w, binary.LittleEndian, c.state.rip)
	binary.Write(w, binary.LittleEndian, c.state.rflags)
	binary.Write(w, binary.LittleEndian, c.state.cs)
	binary.Write(w, binary.LittleEndian, c.state.fs)
	binary.Write(w, binary.LittleEndian, c.state.gs)
}

var amd64Header = machHeader{
	magic:      MH_MAGIC_64,
	cputype:    CPU_TYPE_X86_64,
	cpusubtype: CPU_SUBTYPE_X86_64_ALL | CPU_SUBTYPE_LIB64,
	filetype:   MH_EXECUTE,
	flags:      MH_NOUNDEFS | MH_PIE,
}

var pagezero = loadCmdSegment{
	cmd:      uint32(macho.LoadCmdSegment64),
	cmdsize:  loadCmdSegmentSize,
	segname:  "__PAGEZERO",
	vmaddr:   0,
	vmsize:   0x0000000100000000,
	fileoff:  0,
	filesize: 0,
	maxprot:  0,
	initprot: 0,
	nsects:   0,
	flags:    0,
}

var unixthread = threadCommand{
	cmd:     LC_UNIXTHREAD,
	cmdsize: threadCommandSize,
	flavor:  x86_THREAD_STATE64,
	count:   threadStateDwords,
}

func WriteMacho(exename string, text []byte) error {
	f, err := os.Create(exename)
	if err != nil {
		log.Fatalf("Failed to create file: %s", err)
	}
	defer f.Close()
	amd64Header.ncmds = 3
	amd64Header.sizeofcmds = loadCmdSegmentSize + loadCmdSegmentSize + sectionSize + threadCommandSize
	amd64Header.Write(f)
	pagezero.Write(f)
	size := uint64(((len(text) / 8192) + 2) * 8192)
	machSize := uint64(machHeaderSize + loadCmdSegmentSize + loadCmdSegmentSize + sectionSize + threadCommandSize)
	textsegment := loadCmdSegment{
		cmd:     uint32(macho.LoadCmdSegment64),
		cmdsize: loadCmdSegmentSize + sectionSize,
		segname: "__TEXT",
		vmaddr:  0x0000000100000000,
		vmsize:  size,
		// These commented lines, and the ones below in the textsection
		// result in what appears to be a valid layout, but
		// Result in the error from lldb: "error: Malformed Mach-o file"
		// It is not clear why this is the case.
		// According to the documentation here:// /Library/Developer/CommandLineTools/SDKs/MacOSX10.14.sdk/usr/include/mach-o/loader.h:397
		//   The first segment of a MH_EXECUTE and MH_FVMLIB format file
		//   contains the mach_header and load commands of the object file
		//   before its first section.
		// This seems to imply the first section of an MH_EXECUTABLE *needs*
		// to contain the mach-o header.
		// To test, I should try to add a section before the text section that
		// just contains the mach-o header.
		//fileoff:  machSize,
		//filesize: size + machSize,
		fileoff:  0,
		filesize: size,
		maxprot:  VM_PROT_READ | VM_PROT_WRITE | VM_PROT_EXECUTE,
		initprot: VM_PROT_READ | VM_PROT_EXECUTE,
		nsects:   1,
		flags:    0,
	}
	textsection := section{
		sectname: "__text",
		segname:  "__TEXT",
		//addr:     0x0000000100000000,
		//size:     uint64(len(text)),
		//offset:   uint32(machSize),
		addr:      0x0000000100000000 + machSize,
		size:      uint64(len(text)),
		offset:    uint32(machSize),
		align:     0,
		reloff:    0,
		nreloc:    0,
		flags:     S_ATTR_PURE_INSTRUCTIONS | S_ATTR_SOME_INSTRUCTIONS,
		reserved1: 0,
		reserved2: 0,
		reserved3: 0,
	}
	textsegment.Write(f)
	textsection.Write(f)
	unixthread.state.rip = 0x0000000100000000 + machSize
	//unixthread.state.rip = 0x0000000100000000
	unixthread.Write(f)
	loc, _ := f.Seek(0, 1)
	log.Printf("Writing text at %X", loc)
	log.Printf("Offset: %X", machHeaderSize+loadCmdSegmentSize+loadCmdSegmentSize+sectionSize+threadCommandSize)
	f.Write(text)
	//bs := make([]byte, size-uint64(len(text)))
	bs := make([]byte, size*4)
	f.Write(bs)
	return nil // TODO: Error handling
}
