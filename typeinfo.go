package gbasm

import (
	"encoding/binary"
)

// typeinfo.go: the binary layout for the structured typedesc and iface_desc
// records (PROPOSAL_variadics_and_assertion.md, Layers 1 and 2). bas builds
// these byte blocks + relocations from the new directives; bdump decodes them
// for display. The linker treats them as ordinary Data/Vars byte blocks with
// relocations and needs no knowledge of this layout.

// Record kinds (the Var.Kind tag).
const (
	KindTypedesc      = "typedesc"
	KindTypedescCache = "typedesc_cache"
	KindIfaceDesc     = "iface_desc"
)

// TypedescMethod is one decoded method-table entry of a typedesc.
type TypedescMethod struct {
	Name        string
	Sig         string
	NameHash    uint64
	SigHash     uint64
	RecvShape   uint64
	FnSym       string // relocation target (filled from the Var's Relocs)
}

// TypedescRecord is the in-memory form bas assembles and bdump decodes.
type TypedescRecord struct {
	TypeName  string
	SizeBytes uint64
	CacheSym  string
	Methods   []TypedescMethod
}

// IfaceDescMethod is one decoded required-method entry of an iface_desc.
type IfaceDescMethod struct {
	Name     string
	Sig      string
	NameHash uint64
	SigHash  uint64
	DeclIdx  uint64
}

// IfaceDescRecord is the in-memory form bas assembles and bdump decodes.
type IfaceDescRecord struct {
	IfaceName string
	Methods   []IfaceDescMethod
}

// Field offsets and sizes (bytes).
const (
	tdHeadFixed     = 40 // name_off, name_len, size, cache_ref, method_count
	tdMethodStride  = 64 // 8 u64 fields per method (incl fn_ptr slot)
	tdCacheRefOff   = 24 // offset of cache_ref u64 within the head
	idHeadFixed     = 24 // name_off, name_len, method_count
	idMethodStride  = 56 // 7 u64 fields per req
)

// EncodeTypedesc serializes a TypedescRecord to a byte block plus the
// DataReloc list (cache_ref at offset 24, and one fn_ptr per method). The
// string blob (name + per-method name/sig texts) is appended after the method
// table; offsets are relative to the start of the block.
func EncodeTypedesc(rec *TypedescRecord) ([]byte, []DataReloc) {
	n := len(rec.Methods)
	blobStart := tdHeadFixed + n*tdMethodStride
	var blob []byte
	addStr := func(s string) (off, length uint64) {
		off = uint64(blobStart + len(blob))
		blob = append(blob, []byte(s)...)
		return off, uint64(len(s))
	}
	// Reserve fixed region + method table; fill blob in parallel.
	buf := make([]byte, blobStart)
	putU64 := func(off int, v uint64) { binary.LittleEndian.PutUint64(buf[off:], v) }

	nameOff, nameLen := addStr(rec.TypeName)
	putU64(0, nameOff)
	putU64(8, nameLen)
	putU64(16, rec.SizeBytes)
	putU64(24, 0) // cache_ref slot — filled by reloc
	putU64(32, uint64(n))

	var relocs []DataReloc
	relocs = append(relocs, DataReloc{Offset: tdCacheRefOff, Symbol: rec.CacheSym, Addend: 0})

	for i, m := range rec.Methods {
		base := tdHeadFixed + i*tdMethodStride
		moff, mlen := addStr(m.Name)
		soff, slen := addStr(m.Sig)
		putU64(base+0, moff)
		putU64(base+8, mlen)
		putU64(base+16, soff)
		putU64(base+24, slen)
		putU64(base+32, m.NameHash)
		putU64(base+40, m.SigHash)
		putU64(base+48, m.RecvShape)
		putU64(base+56, 0) // fn_ptr slot — filled by reloc
		relocs = append(relocs, DataReloc{Offset: uint32(base + 56), Symbol: m.FnSym, Addend: 0})
	}
	out := append(buf, blob...)
	return out, relocs
}

// DecodeTypedesc reads back a TypedescRecord from a Var. Relocs supplies the
// cache and fn symbol names by offset.
func DecodeTypedesc(v *Var) *TypedescRecord {
	b := v.Val
	if len(b) < tdHeadFixed {
		return &TypedescRecord{}
	}
	g := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }
	getStr := func(off, length uint64) string {
		if off+length > uint64(len(b)) {
			return ""
		}
		return string(b[off : off+length])
	}
	relAt := func(off uint32) string {
		for _, r := range v.Relocs {
			if r.Offset == off {
				return r.Symbol
			}
		}
		return ""
	}
	rec := &TypedescRecord{
		TypeName:  getStr(g(0), g(8)),
		SizeBytes: g(16),
		CacheSym:  relAt(tdCacheRefOff),
	}
	n := int(g(32))
	for i := 0; i < n; i++ {
		base := tdHeadFixed + i*tdMethodStride
		if base+tdMethodStride > len(b) {
			break
		}
		rec.Methods = append(rec.Methods, TypedescMethod{
			Name:      getStr(g(base+0), g(base+8)),
			Sig:       getStr(g(base+16), g(base+24)),
			NameHash:  g(base + 32),
			SigHash:   g(base + 40),
			RecvShape: g(base + 48),
			FnSym:     relAt(uint32(base + 56)),
		})
	}
	return rec
}

// EncodeIfaceDesc serializes an IfaceDescRecord to a byte block (no relocs).
func EncodeIfaceDesc(rec *IfaceDescRecord) ([]byte, []DataReloc) {
	n := len(rec.Methods)
	blobStart := idHeadFixed + n*idMethodStride
	var blob []byte
	addStr := func(s string) (off, length uint64) {
		off = uint64(blobStart + len(blob))
		blob = append(blob, []byte(s)...)
		return off, uint64(len(s))
	}
	buf := make([]byte, blobStart)
	putU64 := func(off int, v uint64) { binary.LittleEndian.PutUint64(buf[off:], v) }

	nameOff, nameLen := addStr(rec.IfaceName)
	putU64(0, nameOff)
	putU64(8, nameLen)
	putU64(16, uint64(n))

	for i, m := range rec.Methods {
		base := idHeadFixed + i*idMethodStride
		moff, mlen := addStr(m.Name)
		soff, slen := addStr(m.Sig)
		putU64(base+0, moff)
		putU64(base+8, mlen)
		putU64(base+16, soff)
		putU64(base+24, slen)
		putU64(base+32, m.NameHash)
		putU64(base+40, m.SigHash)
		putU64(base+48, m.DeclIdx)
	}
	return append(buf, blob...), nil
}

// DecodeIfaceDesc reads back an IfaceDescRecord from a Var.
func DecodeIfaceDesc(v *Var) *IfaceDescRecord {
	b := v.Val
	if len(b) < idHeadFixed {
		return &IfaceDescRecord{}
	}
	g := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }
	getStr := func(off, length uint64) string {
		if off+length > uint64(len(b)) {
			return ""
		}
		return string(b[off : off+length])
	}
	rec := &IfaceDescRecord{IfaceName: getStr(g(0), g(8))}
	n := int(g(16))
	for i := 0; i < n; i++ {
		base := idHeadFixed + i*idMethodStride
		if base+idMethodStride > len(b) {
			break
		}
		rec.Methods = append(rec.Methods, IfaceDescMethod{
			Name:     getStr(g(base+0), g(base+8)),
			Sig:      getStr(g(base+16), g(base+24)),
			NameHash: g(base + 32),
			SigHash:  g(base + 40),
			DeclIdx:  g(base + 48),
		})
	}
	return rec
}
