package gbasm

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const MaxUint = ^uint(0)
const MinUint = 0
const MaxInt = int(MaxUint >> 1)
const MinInt = -MaxInt - 1

func readSize(r io.Reader) (int, error) {
	var size uint64
	err := binary.Read(r, binary.LittleEndian, &size)
	if err != nil {
		return 0, err
	}
	if size > uint64(MaxInt) {
		return 0, fmt.Errorf("Found size that is too large (%d bytes)", size)
	}
	return int(size), nil
}

func writeSize(w io.Writer, size int) error {
	if size < 0 {
		return fmt.Errorf("Found negative size: %d", size)
	}
	usize := uint64(size)
	return binary.Write(w, binary.LittleEndian, &usize)
}

func writeString(w io.Writer, s string) error {
	bs := []byte(s)
	var size int = len(bs)
	err := writeSize(w, size)
	if err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, bs)
}

func readString(r io.Reader) (string, error) {
	// 	var size int
	// 	err := binary.Read(r, binary.LittleEndian, &size)
	// 	if err != nil {
	// 		return "", err
	// 	}
	// 	if size < 0 {
	// 		return "", fmt.Errorf("Found string that is too large (%d bytes)", size)
	// 	}
	size, err := readSize(r)
	if err != nil {
		return "", err
	}
	bs := make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &bs)
	if err != nil {
		return "", err
	}
	return string(bs), nil
}

func writeTypeDescrs(w io.Writer, types map[string]*TypeDescr) error {
	var size int = len(types)
	err := writeSize(w, size)
	if err != nil {
		return err
	}
	for _, t := range types {
		err := writeTypeDescr(w, t)
		if err != nil {
			return err
		}
	}
	return nil
}

func readTypeDescrs(r io.Reader) (map[string]*TypeDescr, error) {
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*TypeDescr)
	for i := 0; i < size; i++ {
		t, err := readTypeDescr(r)
		if err != nil {
			return nil, err
		}
		m[t.Name] = t
	}
	return m, nil
}

func writeTypeDescr(w io.Writer, t *TypeDescr) error {
	err := writeString(w, t.Name)
	if err != nil {
		return err
	}
	var size int = len(t.Properties)
	err = writeSize(w, size)
	if err != nil {
		return err
	}
	for _, p := range t.Properties {
		err := writeString(w, p)
		if err != nil {
			return err
		}
	}
	size = len(t.Description)
	err = writeSize(w, size)
	if err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, t.Description)
}

func readTypeDescr(r io.Reader) (*TypeDescr, error) {
	name, err := readString(r)
	if err != nil {
		return nil, err
	}
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	ps := make([]string, size)
	for i := range ps {
		s, err := readString(r)
		if err != nil {
			return nil, err
		}
		ps[i] = s
	}
	size, err = readSize(r)
	if err != nil {
		return nil, err
	}
	bs := make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &bs)
	if err != nil {
		return nil, err
	}
	return &TypeDescr{Name: name, Properties: ps, Description: bs}, nil
}

func writeVars(w io.Writer, vs map[string]*Var) error {
	var size int = len(vs)
	err := writeSize(w, size)
	if err != nil {
		return err
	}
	for _, t := range vs {
		err := writeVar(w, t)
		if err != nil {
			return err
		}
	}
	return nil
}

func readVars(r io.Reader) (map[string]*Var, error) {
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*Var)
	for i := 0; i < size; i++ {
		t, err := readVar(r)
		if err != nil {
			return nil, err
		}
		m[t.Name] = t
	}
	return m, nil
}

func writeVar(w io.Writer, v *Var) error {
	err := writeString(w, v.Name)
	if err != nil {
		return err
	}
	err = writeString(w, v.VType)
	if err != nil {
		return err
	}
	err = writeSize(w, len(v.Val))
	if err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, v.Val)
}

func readVar(r io.Reader) (*Var, error) {
	name, err := readString(r)
	if err != nil {
		return nil, err
	}
	vtype, err := readString(r)
	if err != nil {
		return nil, err
	}
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	bs := make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &bs)
	if err != nil {
		return nil, err
	}
	return &Var{Name: name, VType: vtype, Val: bs}, nil
}

func writeSymbol(w io.Writer, v *Symbol) error {
	err := writeString(w, v.Name)
	if err != nil {
		return err
	}
	err = binary.Write(w, binary.LittleEndian, v.Offset)
	if err != nil {
		return err
	}
	return nil
}

func readSymbol(r io.Reader) (Symbol, error) {
	name, err := readString(r)
	if err != nil {
		return Symbol{}, err
	}
	var offset uint32
	err = binary.Read(r, binary.LittleEndian, &offset)
	if err != nil {
		return Symbol{}, err
	}
	return Symbol{Name: name, Offset: offset}, nil
}

func writeRelocation(w io.Writer, v *Relocation) error {
	if err := binary.Write(w, binary.LittleEndian, v.Offset); err != nil {
		return err
	}
	if err := writeString(w, v.Symbol); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, v.Addend); err != nil {
		return err
	}
	return nil
}

func readRelocation(r io.Reader) (Relocation, error) {
	var rel Relocation
	if err := binary.Read(r, binary.LittleEndian, &rel.Offset); err != nil {
		return Relocation{}, err
	}
	sym, err := readString(r)
	if err != nil {
		return Relocation{}, err
	}
	rel.Symbol = sym
	if err := binary.Read(r, binary.LittleEndian, &rel.Addend); err != nil {
		return Relocation{}, err
	}
	return rel, nil
}

func writeFunctions(w io.Writer, fs map[string]*Function) error {
	var size int = len(fs)
	err := writeSize(w, size)
	if err != nil {
		return err
	}
	for _, f := range fs {
		err := writeFunction(w, f)
		if err != nil {
			return fmt.Errorf("Writing function %s: %w", f.Name, err)
		}
	}
	return nil
}

func readFunctions(r io.Reader) (map[string]*Function, error) {
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*Function)
	for i := 0; i < size; i++ {
		f, err := readFunction(r)
		if err != nil {
			return nil, err
		}
		m[f.Name] = f
	}
	return m, nil
}

func writeFunction(w io.Writer, f *Function) error {
	if err := f.Resolve(); err != nil {
		return fmt.Errorf("While resolving: %w", err)
	}
	err := writeString(w, f.Name)
	if err != nil {
		return fmt.Errorf("Writing name: %w", err)
	}
	err = writeString(w, f.Type)
	if err != nil {
		return fmt.Errorf("Writing type: %w", err)
	}
	err = writeString(w, f.SrcFile)
	if err != nil {
		return fmt.Errorf("Writing srcfile: %w", err)
	}
	err = writeSize(w, f.SrcLine)
	if err != nil {
		return fmt.Errorf("Writing srcline: %w", err)
	}
	size := len(f.Args)
	err = writeSize(w, size)
	if err != nil {
		return fmt.Errorf("Writing args size: %w", err)
	}
	for _, v := range f.Args {
		err := writeVar(w, v)
		if err != nil {
			return fmt.Errorf("Writing args: %w", err)
		}
	}
	size = len(f.Symbols)
	err = writeSize(w, size)
	if err != nil {
		return fmt.Errorf("Writing symbols size: %w", err)
	}
	for _, v := range f.Symbols {
		err := writeSymbol(w, &v)
		if err != nil {
			return fmt.Errorf("Writing symbol: %w", err)
		}
	}
	size = len(f.Relocations)
	err = writeSize(w, size)
	if err != nil {
		return fmt.Errorf("Writing relocations size: %w", err)
	}
	for _, r := range f.Relocations {
		//log.Printf("Writing relocation: %#v\n", r)
		err := writeRelocation(w, &r)
		if err != nil {
			return fmt.Errorf("While writing relocation: %w", err)
		}
	}
	body, err := f.Body()
	if err != nil {
		return fmt.Errorf("Generating body: %w\n", err)
	}
	size = len(body)
	err = writeSize(w, size)
	if err != nil {
		return fmt.Errorf("writing body size: %w", err)
	}
	err = binary.Write(w, binary.LittleEndian, f.bodyBs)
	if err != nil {
		return fmt.Errorf("Writing body: %w", err)
	}
	return nil
}

func readFunction(r io.Reader) (*Function, error) {
	name, err := readString(r)
	if err != nil {
		return nil, err
	}
	fType, err := readString(r)
	if err != nil {
		return nil, err
	}
	srcFile, err := readString(r)
	if err != nil {
		return nil, err
	}
	srcLine, err := readSize(r)
	if err != nil {
		return nil, err
	}
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	args := make([]*Var, size)
	for i := range args {
		v, err := readVar(r)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	size, err = readSize(r)
	if err != nil {
		return nil, err
	}
	symbols := make([]Symbol, size)
	for i := range symbols {
		s, err := readSymbol(r)
		if err != nil {
			return nil, err
		}
		symbols[i] = s
	}
	size, err = readSize(r)
	if err != nil {
		return nil, err
	}
	relocations := make([]Relocation, size)
	for i := range relocations {
		rr, err := readRelocation(r)
		if err != nil {
			return nil, err
		}
		//log.Printf("Reading relocation: %#v\n", rr)
		relocations[i] = rr
	}
	size, err = readSize(r)
	if err != nil {
		return nil, err
	}
	bodyBs := make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &bodyBs)
	if err != nil {
		return nil, err
	}
	return &Function{
		Name:        name,
		Type:        fType,
		SrcFile:     srcFile,
		SrcLine:     srcLine,
		Args:        args,
		Symbols:     symbols,
		Relocations: relocations,
		bodyBs:      bodyBs,
	}, nil
}

func writeOFile(w io.Writer, o *OFile) error {
	err := writeString(w, o.Pkgname)
	if err != nil {
		return err
	}
	err = writeString(w, o.ExeFormat)
	if err != nil {
		return err
	}
	err = writeTypeDescrs(w, o.Types)
	if err != nil {
		return err
	}
	err = writeVars(w, o.Data)
	if err != nil {
		return err
	}
	err = writeVars(w, o.Vars)
	if err != nil {
		return err
	}
	err = writeFunctions(w, o.Funcs)
	if err != nil {
		return fmt.Errorf("WriteFunctions: %w", err)
	}
	err = writeStructs(w, o.Structs)
	if err != nil {
		return err
	}
	return nil
}

func readOFile(r io.Reader) (*OFile, error) {
	pkgname, err := readString(r)
	if err != nil {
		return nil, err
	}
	exeformat, err := readString(r)
	if err != nil {
		return nil, err
	}
	types, err := readTypeDescrs(r)
	if err != nil {
		return nil, err
	}
	data, err := readVars(r)
	if err != nil {
		return nil, err
	}
	vars, err := readVars(r)
	if err != nil {
		return nil, err
	}
	funcs, err := readFunctions(r)
	if err != nil {
		return nil, err
	}
	structs, err := readStructs(r)
	if err != nil {
		return nil, err
	}
	return &OFile{
		Pkgname:   pkgname,
		ExeFormat: exeformat,
		Types:     types,
		Data:      data,
		Vars:      vars,
		Funcs:     funcs,
		Structs:   structs,
	}, nil
}

func writeStructs(w io.Writer, ss map[string]*StructShape) error {
	if err := writeSize(w, len(ss)); err != nil {
		return err
	}
	// Sort for deterministic output — map iteration is randomized in Go.
	names := make([]string, 0, len(ss))
	for n := range ss {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s := ss[name]
		if err := writeString(w, s.Name); err != nil {
			return err
		}
		if err := writeSize(w, len(s.Fields)); err != nil {
			return err
		}
		for _, f := range s.Fields {
			if err := writeString(w, f.Name); err != nil {
				return err
			}
			if err := writeString(w, f.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

func readStructs(r io.Reader) (map[string]*StructShape, error) {
	n, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*StructShape, n)
	for i := 0; i < n; i++ {
		name, err := readString(r)
		if err != nil {
			return nil, err
		}
		nFields, err := readSize(r)
		if err != nil {
			return nil, err
		}
		fields := make([]FieldShape, nFields)
		for j := 0; j < nFields; j++ {
			fName, err := readString(r)
			if err != nil {
				return nil, err
			}
			fType, err := readString(r)
			if err != nil {
				return nil, err
			}
			fields[j] = FieldShape{Name: fName, Type: fType}
		}
		m[name] = &StructShape{Name: name, Fields: fields}
	}
	return m, nil
}
