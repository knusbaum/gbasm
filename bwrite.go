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
	if err := writeString(w, v.Name); err != nil {
		return err
	}
	if err := writeString(w, v.VType); err != nil {
		return err
	}
	if err := writeSize(w, len(v.Val)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, v.Val); err != nil {
		return err
	}
	if err := writeSize(w, len(v.Relocs)); err != nil {
		return err
	}
	for _, r := range v.Relocs {
		if err := writeDataReloc(w, &r); err != nil {
			return err
		}
	}
	return nil
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
	if err := binary.Read(r, binary.LittleEndian, &bs); err != nil {
		return nil, err
	}
	nrelocs, err := readSize(r)
	if err != nil {
		return nil, err
	}
	relocs := make([]DataReloc, 0, nrelocs)
	for i := 0; i < nrelocs; i++ {
		dr, err := readDataReloc(r)
		if err != nil {
			return nil, err
		}
		relocs = append(relocs, dr)
	}
	return &Var{Name: name, VType: vtype, Val: bs, Relocs: relocs}, nil
}

func writeDataReloc(w io.Writer, r *DataReloc) error {
	if err := binary.Write(w, binary.LittleEndian, r.Offset); err != nil {
		return err
	}
	if err := writeString(w, r.Symbol); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, r.Addend)
}

func readDataReloc(r io.Reader) (DataReloc, error) {
	var dr DataReloc
	if err := binary.Read(r, binary.LittleEndian, &dr.Offset); err != nil {
		return DataReloc{}, err
	}
	sym, err := readString(r)
	if err != nil {
		return DataReloc{}, err
	}
	dr.Symbol = sym
	if err := binary.Read(r, binary.LittleEndian, &dr.Addend); err != nil {
		return DataReloc{}, err
	}
	return dr, nil
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
	err = writeTypeAliases(w, o.TypeAliases)
	if err != nil {
		return err
	}
	err = writeInterfaces(w, o.Interfaces)
	if err != nil {
		return err
	}
	err = writeValues(w, o.Values)
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
	typeAliases, err := readTypeAliases(r)
	if err != nil {
		return nil, err
	}
	interfaces, err := readInterfaces(r)
	if err != nil {
		return nil, err
	}
	values, err := readValues(r)
	if err != nil {
		return nil, err
	}
	return &OFile{
		Pkgname:     pkgname,
		ExeFormat:   exeformat,
		Types:       types,
		Data:        data,
		Vars:        vars,
		Funcs:       funcs,
		Structs:     structs,
		TypeAliases: typeAliases,
		Interfaces:  interfaces,
		Values:      values,
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

func writeTypeAliases(w io.Writer, aliases map[string]*TypeAliasShape) error {
	names := make([]string, 0, len(aliases))
	for n := range aliases {
		names = append(names, n)
	}
	sort.Strings(names)
	if err := writeSize(w, len(names)); err != nil {
		return err
	}
	for _, name := range names {
		a := aliases[name]
		if err := writeString(w, a.Name); err != nil {
			return err
		}
		if err := writeString(w, a.Underlying); err != nil {
			return err
		}
		if err := writeSize(w, len(a.MethodNames)); err != nil {
			return err
		}
		for _, mn := range a.MethodNames {
			if err := writeString(w, mn); err != nil {
				return err
			}
		}
	}
	return nil
}

func readTypeAliases(r io.Reader) (map[string]*TypeAliasShape, error) {
	n, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*TypeAliasShape, n)
	for i := 0; i < n; i++ {
		name, err := readString(r)
		if err != nil {
			return nil, err
		}
		underlying, err := readString(r)
		if err != nil {
			return nil, err
		}
		nMethods, err := readSize(r)
		if err != nil {
			return nil, err
		}
		methods := make([]string, nMethods)
		for j := range methods {
			mn, err := readString(r)
			if err != nil {
				return nil, err
			}
			methods[j] = mn
		}
		m[name] = &TypeAliasShape{Name: name, Underlying: underlying, MethodNames: methods}
	}
	return m, nil
}

func writeInterfaces(w io.Writer, ifaces map[string]*InterfaceShape) error {
	names := make([]string, 0, len(ifaces))
	for n := range ifaces {
		names = append(names, n)
	}
	sort.Strings(names)
	if err := writeSize(w, len(names)); err != nil {
		return err
	}
	for _, name := range names {
		ifc := ifaces[name]
		if err := writeString(w, ifc.Name); err != nil {
			return err
		}
		if err := writeSize(w, len(ifc.Methods)); err != nil {
			return err
		}
		for _, m := range ifc.Methods {
			if err := writeString(w, m.Name); err != nil {
				return err
			}
			if err := writeSize(w, len(m.Params)); err != nil {
				return err
			}
			for _, p := range m.Params {
				if err := writeString(w, p.Name); err != nil {
					return err
				}
				if err := writeString(w, p.Type); err != nil {
					return err
				}
			}
			if err := writeString(w, m.Return); err != nil {
				return err
			}
		}
	}
	return nil
}

func readInterfaces(r io.Reader) (map[string]*InterfaceShape, error) {
	n, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*InterfaceShape, n)
	for i := 0; i < n; i++ {
		name, err := readString(r)
		if err != nil {
			return nil, err
		}
		nMethods, err := readSize(r)
		if err != nil {
			return nil, err
		}
		methods := make([]InterfaceMethodShape, nMethods)
		for j := range methods {
			mname, err := readString(r)
			if err != nil {
				return nil, err
			}
			nParams, err := readSize(r)
			if err != nil {
				return nil, err
			}
			params := make([]FieldShape, nParams)
			for k := range params {
				pname, err := readString(r)
				if err != nil {
					return nil, err
				}
				ptype, err := readString(r)
				if err != nil {
					return nil, err
				}
				params[k] = FieldShape{Name: pname, Type: ptype}
			}
			ret, err := readString(r)
			if err != nil {
				return nil, err
			}
			methods[j] = InterfaceMethodShape{Name: mname, Params: params, Return: ret}
		}
		m[name] = &InterfaceShape{Name: name, Methods: methods}
	}
	return m, nil
}

func writeValues(w io.Writer, vs map[string]*ValuesShape) error {
	names := make([]string, 0, len(vs))
	for n := range vs {
		names = append(names, n)
	}
	sort.Strings(names)
	if err := writeSize(w, len(names)); err != nil {
		return err
	}
	for _, name := range names {
		v := vs[name]
		if err := writeString(w, v.Name); err != nil {
			return err
		}
		if err := writeString(w, v.TagType); err != nil {
			return err
		}
		if err := writeSize(w, len(v.Cases)); err != nil {
			return err
		}
		for _, vc := range v.Cases {
			if err := writeString(w, vc.Name); err != nil {
				return err
			}
			if err := binary.Write(w, binary.LittleEndian, vc.Tag); err != nil {
				return err
			}
		}
		if err := writeSize(w, len(v.Projections)); err != nil {
			return err
		}
		for _, pj := range v.Projections {
			if err := writeString(w, pj.TargetType); err != nil {
				return err
			}
		}
		if err := writeSize(w, len(v.MethodNames)); err != nil {
			return err
		}
		for _, mn := range v.MethodNames {
			if err := writeString(w, mn); err != nil {
				return err
			}
		}
	}
	return nil
}

func readValues(r io.Reader) (map[string]*ValuesShape, error) {
	n, err := readSize(r)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*ValuesShape, n)
	for i := 0; i < n; i++ {
		name, err := readString(r)
		if err != nil {
			return nil, err
		}
		tagType, err := readString(r)
		if err != nil {
			return nil, err
		}
		nCases, err := readSize(r)
		if err != nil {
			return nil, err
		}
		cases := make([]ValuesCaseShape, nCases)
		for j := range cases {
			cname, err := readString(r)
			if err != nil {
				return nil, err
			}
			var tag int64
			if err := binary.Read(r, binary.LittleEndian, &tag); err != nil {
				return nil, err
			}
			cases[j] = ValuesCaseShape{Name: cname, Tag: tag}
		}
		nProj, err := readSize(r)
		if err != nil {
			return nil, err
		}
		projs := make([]ProjectionShape, nProj)
		for j := range projs {
			tt, err := readString(r)
			if err != nil {
				return nil, err
			}
			projs[j] = ProjectionShape{TargetType: tt}
		}
		nMethods, err := readSize(r)
		if err != nil {
			return nil, err
		}
		methods := make([]string, nMethods)
		for j := range methods {
			mn, err := readString(r)
			if err != nil {
				return nil, err
			}
			methods[j] = mn
		}
		m[name] = &ValuesShape{
			Name:        name,
			TagType:     tagType,
			Cases:       cases,
			Projections: projs,
			MethodNames: methods,
		}
	}
	return m, nil
}
