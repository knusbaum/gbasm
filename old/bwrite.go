package gbasm

import (
	"encoding/binary"
	"fmt"
	"io"
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
		m[t.name] = t
	}
	return m, nil
}

func writeTypeDescr(w io.Writer, t *TypeDescr) error {
	err := writeString(w, t.name)
	if err != nil {
		return err
	}
	var size int = len(t.properties)
	err = writeSize(w, size)
	if err != nil {
		return err
	}
	for _, p := range t.properties {
		err := writeString(w, p)
		if err != nil {
			return err
		}
	}
	size = len(t.description)
	err = writeSize(w, size)
	if err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, t.description)
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
	return &TypeDescr{name: name, properties: ps, description: bs}, nil
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
		m[t.name] = t
	}
	return m, nil
}

func writeVar(w io.Writer, v *Var) error {
	err := writeString(w, v.name)
	if err != nil {
		return err
	}
	err = writeString(w, v.vtype)
	if err != nil {
		return err
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
	return &Var{name: name, vtype: vtype}, nil
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
			return err
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
		m[f.name] = f
	}
	return m, nil
}

func writeFunction(w io.Writer, f *Function) error {
	err := writeString(w, f.name)
	if err != nil {
		return err
	}
	err = writeString(w, f.srcFile)
	if err != nil {
		return err
	}
	err = writeSize(w, f.srcLine)
	if err != nil {
		return err
	}
	size := len(f.args)
	err = writeSize(w, size)
	if err != nil {
		return err
	}
	for _, v := range f.args {
		err := writeVar(w, v)
		if err != nil {
			return err
		}
	}
	if f.bodyBs != nil {
		// function already compiled.
		size := len(f.bodyBs)
		err = writeSize(w, size)
		if err != nil {
			return err
		}
		err = binary.Write(w, binary.LittleEndian, f.bodyBs)
		if err != nil {
			return err
		}
	} else {
		bs, err := assemble(f.body)
		if err != nil {
			return err
		}
		size := len(bs)
		err = writeSize(w, size)
		if err != nil {
			return err
		}
		err = binary.Write(w, binary.LittleEndian, bs)
		if err != nil {
			return err
		}
	}
	return nil
}

func readFunction(r io.Reader) (*Function, error) {
	name, err := readString(r)
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
	bodyBs := make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &bodyBs)
	if err != nil {
		return nil, err
	}
	return &Function{
		name:    name,
		srcFile: srcFile,
		srcLine: srcLine,
		args:    args,
		bodyBs:  bodyBs,
	}, nil
}

func writeOFile(w io.Writer, o *OFile) error {
	err := writeString(w, o.pkgname)
	if err != nil {
		return err
	}
	err = writeString(w, o.exeformat)
	if err != nil {
		return err
	}
	err = writeTypeDescrs(w, o.types)
	if err != nil {
		return err
	}
	err = writeVars(w, o.data)
	if err != nil {
		return err
	}
	err = writeVars(w, o.vars)
	if err != nil {
		return err
	}
	err = writeFunctions(w, o.funcs)
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
	return &OFile{
		pkgname:   pkgname,
		exeformat: exeformat,
		types:     types,
		data:      data,
		vars:      vars,
		funcs:     funcs,
	}, nil
}
