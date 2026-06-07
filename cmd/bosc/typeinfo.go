package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// typeinfo.go: emission of the structured typedesc / iface_desc / cache
// symbols described in PROPOSAL_variadics_and_assertion.md (Layers 1 and 2).
//
// The compiler computes method-name and non-receiver-signature hashes (64-bit
// FNV-1a) and the canonical receiver-shape encoding, then emits the three new
// bas directives (typedesc / typedesc_cache / iface_desc). bas owns the binary
// layout; bdump pretty-prints it. The compile-time satisfaction filter and the
// runtime helper (_iface.assert_to) both consult the same canonical
// signature-text rendering and the same shape-word/receiver-shape derivation
// so the two paths agree exactly.

const fnvOffset64 = 1469598103934665603
const fnvPrime64 = 1099511628211

// fnv1a64 computes the 64-bit FNV-1a hash of s. Deterministic across builds
// and stateless, so the same input bytes always hash identically regardless of
// which compilation unit performs the hash.
func fnv1a64(s string) uint64 {
	var h uint64 = fnvOffset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

// canonicalTypeName fully-qualifies a single base type name for signature
// canonicalization. Named user types declared in the current package are
// rendered bare (e.g. "Point") on the producer side but qualified
// ("geom.Point") on the consumer side; left unqualified, a method whose
// signature mentions such a type would hash differently in the typedesc
// (producer) and the iface_desc (consumer), so the cross-package assertion
// would silently miss. We canonicalize to the always-qualified "pkg.Name" form:
//
//   - a name that already contains '.' is already qualified — keep it.
//   - a universal/built-in name (scalars, bool, byte, void, self) is rendered
//     identically in every package — keep it bare.
//   - any other bare name is a user type local to the current package
//     (cross-package references are always written qualified in source), so
//     prepend the current package name.
func canonicalTypeName(c *Context, name string) string {
	if name == "" || strings.Contains(name, ".") {
		return name
	}
	switch name {
	case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64",
		"byte", "bool", "void", "self", "any":
		return name
	}
	if c == nil {
		return name
	}
	return c.Pkgname() + "." + name
}

// canonicalTypeString renders an ASTType with every named base canonicalized to
// its fully-qualified form (see canonicalTypeName). It mirrors ASTType.String's
// structure but substitutes the qualified name for the leaf, so the producer
// (typedesc) and consumer (iface_desc) sides agree byte-for-byte.
func canonicalTypeString(c *Context, t ASTType) string {
	if t.Name != "" && t.AnonFields == nil && t.FuncSig == nil {
		// A simple named (possibly pointer/slice/array-wrapped) type: rewrite
		// the leaf name to its qualified form and let String() do the rest.
		qt := t
		qt.Name = canonicalTypeName(c, t.Name)
		return qt.String()
	}
	if t.Element != nil {
		// Composite (slice/array of something): rewrite the leaf recursively.
		qt := t
		elem := canonicalTypeStringElem(c, *t.Element)
		qt.Element = &elem
		return qt.String()
	}
	// Anonymous structs / function pointers: fall back to the plain rendering.
	return t.String()
}

// canonicalTypeStringElem returns a copy of t with its named leaf qualified,
// for use as a slice/array element. (We cannot call canonicalTypeString here
// because it returns a rendered string; we need a rebuilt ASTType.)
func canonicalTypeStringElem(c *Context, t ASTType) ASTType {
	qt := t
	if t.Element != nil {
		elem := canonicalTypeStringElem(c, *t.Element)
		qt.Element = &elem
	} else if t.Name != "" {
		qt.Name = canonicalTypeName(c, t.Name)
	}
	return qt
}

// canonicalSig renders the canonical non-receiver signature text for a method:
// the parameter types after the receiver, then "->", then the return type.
// Both the source typedesc method and the interface required method render
// through this single function so a satisfying pair produces byte-identical
// strings. The receiver slot is intentionally excluded — receiver direction is
// matched on the separate receiver-shape axis. Named types are fully-qualified
// (canonicalTypeName) so a cross-package signature renders identically on the
// producer and consumer sides.
func canonicalSig(c *Context, params []Binding, ret ASTType) string {
	var sb strings.Builder
	sb.WriteString("(")
	for i, p := range params {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(canonicalTypeString(c, p.Type))
	}
	sb.WriteString(")->")
	sb.WriteString(canonicalTypeString(c, ret))
	return sb.String()
}

// methodEntry is one row of a typedesc method table.
type methodEntry struct {
	name        string
	sig         string
	nameHash    uint64
	sigHash     uint64
	recvShape   int
	fnReloc     string // fully-qualified symbol of the method body
}

// reqEntry is one row of an iface_desc required-method table.
type reqEntry struct {
	name     string
	sig      string
	nameHash uint64
	sigHash  uint64
	declIdx  int // declaration index, used to place the fn in the itab vtable
}

// typedescDirectiveName returns the directive symbol name for a base type's
// typedesc (without package qualification).
func typedescDirectiveName(typeName string) string {
	return typedescSymbolName(typeName)
}

func typedescCacheName(typeName string) string {
	return "__typedesc_cache_" + strings.ReplaceAll(typeName, ".", "_")
}

func ifaceDescName(ifaceName string) string {
	return "__iface_desc_" + strings.ReplaceAll(ifaceName, ".", "_")
}

// emitTypedescStructured writes the typedesc + typedesc_cache directive pair
// for a base type whose method set is `methods`. sizeBytes is sizeof(T).
func emitTypedescStructured(of io.Writer, isPub bool, typeName string, sizeBytes int, methods []methodEntry) {
	// Sort by (name_hash, sig_hash, receiver_shape) ascending.
	sort.Slice(methods, func(i, j int) bool {
		if methods[i].nameHash != methods[j].nameHash {
			return methods[i].nameHash < methods[j].nameHash
		}
		if methods[i].sigHash != methods[j].sigHash {
			return methods[i].sigHash < methods[j].sigHash
		}
		return methods[i].recvShape < methods[j].recvShape
	})
	name := typedescDirectiveName(typeName)
	cacheName := typedescCacheName(typeName)
	fmt.Fprintf(of, "%stypedesc %s {\n", pubPrefix(isPub), name)
	fmt.Fprintf(of, "\tname \"%s\"\n", typeName)
	fmt.Fprintf(of, "\tsize %d\n", sizeBytes)
	fmt.Fprintf(of, "\tcache_ref %s\n", cacheName)
	for _, m := range methods {
		fmt.Fprintf(of, "\tmethod %s %s %d %d %d %s\n",
			m.name, basQuoteSig(m.sig), m.nameHash, m.sigHash, m.recvShape, m.fnReloc)
	}
	fmt.Fprintf(of, "}\n")
	fmt.Fprintf(of, "%stypedesc_cache %s\n", pubPrefix(isPub), cacheName)
}

// emitIfaceDescStructured writes the iface_desc directive for an interface.
func emitIfaceDescStructured(of io.Writer, isPub bool, ifaceName string, reqs []reqEntry) {
	sort.Slice(reqs, func(i, j int) bool {
		if reqs[i].nameHash != reqs[j].nameHash {
			return reqs[i].nameHash < reqs[j].nameHash
		}
		return reqs[i].sigHash < reqs[j].sigHash
	})
	name := ifaceDescName(ifaceName)
	fmt.Fprintf(of, "%siface_desc %s {\n", pubPrefix(isPub), name)
	fmt.Fprintf(of, "\tname \"%s\"\n", ifaceName)
	for _, r := range reqs {
		fmt.Fprintf(of, "\tmethod %s %s %d %d %d\n",
			r.name, basQuoteSig(r.sig), r.nameHash, r.sigHash, r.declIdx)
	}
	fmt.Fprintf(of, "}\n")
}

// basQuoteSig renders a signature text as a single whitespace-free token for
// the bas directive. A canonical signature CAN contain spaces (ASTType.String
// emits "*mut " and "owned " with trailing spaces), and bas tokenizes the
// directive on whitespace, so any space in the signature would split the token.
// We map each space to the non-printable separator byte 0x1f (US, "unit
// separator"), which never appears in a type rendering; bas reverses the
// mapping with basUnquoteSig to recover the exact signature text. The mapping
// is a fixed byte<->byte substitution, applied identically on both sides, so
// the producer and consumer always agree byte-for-byte.
func basQuoteSig(sig string) string {
	return strings.ReplaceAll(sig, " ", "\x1f")
}

// basUnquoteSig is the inverse of basQuoteSig (used by bas).
func basUnquoteSig(tok string) string {
	return strings.ReplaceAll(tok, "\x1f", " ")
}

// methodEntriesForType builds the typedesc method-table rows for a named type,
// using its full method set. Static (receiver-less) methods are skipped — they
// aren't callable through an interface. The owner-package qualifies the fn
// relocation symbol.
func methodEntriesForType(c *Context, typeName, ownerPkg string) []methodEntry {
	methods, ok := c.TypeMethodsFor(typeName)
	if !ok {
		return nil
	}
	var out []methodEntry
	for _, m := range methods {
		if len(m.Args) == 0 {
			continue // static method, no receiver
		}
		recv := m.Args[0].Type
		rshape := receiverShapeOf(recv)
		sig := canonicalSig(c, m.Args[1:], m.Return)
		out = append(out, methodEntry{
			name:      m.Name,
			sig:       sig,
			nameHash:  fnv1a64(m.Name),
			sigHash:   fnv1a64(sig),
			recvShape: rshape,
			fnReloc:   fmt.Sprintf("%s.%s.%s", ownerPkg, typeName, m.Name),
		})
	}
	return out
}

// reqEntriesForInterface builds the iface_desc required-method rows. c is the
// declaring interface's context, used only to canonicalize named types in the
// signature text (see canonicalSig).
func reqEntriesForInterface(c *Context, iface *InterfaceDecl) []reqEntry {
	var out []reqEntry
	for i, m := range iface.Methods {
		// Params[0] is the receiver; the non-receiver signature is Params[1:].
		var nonRecv []Binding
		if len(m.Params) > 0 {
			nonRecv = m.Params[1:]
		}
		sig := canonicalSig(c, nonRecv, m.Return)
		out = append(out, reqEntry{
			name:     m.Name,
			sig:      sig,
			nameHash: fnv1a64(m.Name),
			sigHash:  fnv1a64(sig),
			declIdx:  i,
		})
	}
	return out
}
