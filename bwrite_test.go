package gbasm

import (
	"bytes"
	"testing"
)

// TestStructShapeMethodNamesRoundTrip verifies that the MethodNames field
// added to StructShape (so cross-package importers can reconstruct a struct's
// method table) survives serialization through writeOFile/readOFile intact,
// including the methodless case.
func TestStructShapeMethodNamesRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name    string
		methods []string
	}{
		{name: "WithMethods", methods: []string{"describe", "format", "string"}},
		{name: "Methodless", methods: nil},
		{name: "EmptySlice", methods: []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, err := NewOFile("test.bo", "mypkg")
			if err != nil {
				t.Fatalf("NewOFile: %v", err)
			}
			fields := []FieldShape{
				{Name: "x", Type: "i64"},
				{Name: "y", Type: "byte[]"},
			}
			if err := o.AddStruct("Point", fields, tt.methods, true); err != nil {
				t.Fatalf("AddStruct: %v", err)
			}

			var buf bytes.Buffer
			if err := writeOFile(&buf, o); err != nil {
				t.Fatalf("writeOFile: %v", err)
			}
			got, err := readOFile(&buf)
			if err != nil {
				t.Fatalf("readOFile: %v", err)
			}

			s := got.Structs["Point"]
			if s == nil {
				t.Fatalf("struct Point missing after round-trip")
			}
			if s.Name != "Point" || !s.IsPub {
				t.Fatalf("struct header garbled: Name=%q IsPub=%v", s.Name, s.IsPub)
			}
			if len(s.Fields) != 2 || s.Fields[0].Name != "x" || s.Fields[1].Name != "y" {
				t.Fatalf("fields garbled: %+v", s.Fields)
			}
			// The methodless and empty-slice cases both deserialize to an
			// empty (length-0) slice; an importer iterating it gets nothing,
			// which is the intended behavior.
			if len(s.MethodNames) != len(tt.methods) {
				t.Fatalf("MethodNames length: got %d (%v), want %d (%v)",
					len(s.MethodNames), s.MethodNames, len(tt.methods), tt.methods)
			}
			for i := range tt.methods {
				if s.MethodNames[i] != tt.methods[i] {
					t.Fatalf("MethodNames[%d]: got %q want %q", i, s.MethodNames[i], tt.methods[i])
				}
			}
		})
	}
}
