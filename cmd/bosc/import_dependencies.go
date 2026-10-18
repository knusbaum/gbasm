package main

import (
	"regexp"
	"sort"

	"github.com/knusbaum/gbasm"
)

var qualifiedMetadataType = regexp.MustCompile(`\b([A-Za-z_][A-Za-z_0-9]*)\.`)

// Exported signatures can mention types from a package the consumer does not
// explicitly import. Load that metadata without exposing its source qualifier.
func metadataDependencies(obj *gbasm.OFile) []string {
	deps := make(map[string]bool)
	add := func(text string) {
		for _, match := range qualifiedMetadataType.FindAllStringSubmatch(text, -1) {
			if match[1] != obj.Pkgname {
				deps[match[1]] = true
			}
		}
	}
	for _, fn := range obj.Funcs {
		if fn.IsPub {
			add(fn.Type)
		}
	}
	for _, v := range obj.Vars {
		if v.IsPub {
			add(v.VType)
		}
	}
	for _, st := range obj.Structs {
		if st.IsPub {
			for _, f := range st.Fields {
				add(f.Type)
			}
		}
	}
	for _, alias := range obj.TypeAliases {
		if alias.IsPub {
			add(alias.Underlying)
		}
	}
	for _, iface := range obj.Interfaces {
		if !iface.IsPub {
			continue
		}
		for _, method := range iface.Methods {
			add(method.Return)
			for _, p := range method.Params {
				add(p.Type)
			}
		}
	}
	for _, values := range obj.Values {
		if !values.IsPub {
			continue
		}
		add(values.TagType)
		for _, projection := range values.Projections {
			add(projection.TargetType)
		}
		for _, variant := range values.Cases {
			for _, f := range variant.Payload {
				add(f.Type)
			}
		}
	}
	var out []string
	for dep := range deps {
		out = append(out, dep)
	}
	sort.Strings(out)
	return out
}

type packageCompileError struct {
	phase string
	err   error
}

func (e *packageCompileError) Error() string { return e.err.Error() }
func (e *packageCompileError) Unwrap() error { return e.err }
