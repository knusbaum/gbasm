package main

import (
	"strings"
	"testing"
)

// TestScanValuesDecl confirms parseBosType recognizes the values form
// added in Stage 6 and captures the cases block plus methods. It scans
// the runtime/errors fixture rather than a synthetic file so the
// regression catches drift in the real producer source.
func TestScanValuesDecl(t *testing.T) {
	ps, err := ScanPackage("../../runtime/errors", "errors")
	if err != nil {
		t.Fatalf("ScanPackage: %v", err)
	}
	var got Decl
	for _, d := range ps.Decls {
		if d.Kind == DeclType && d.Name == "io_error" {
			got = d
			break
		}
	}
	if got.Name == "" {
		t.Fatalf("expected to find DeclType io_error; got decls: %+v", ps.Decls)
	}
	if !strings.Contains(got.Body, "NOT_FOUND") {
		t.Errorf("expected case NOT_FOUND in body, got: %q", got.Body)
	}
	if !strings.Contains(got.Body, "PERMISSION_DENIED") {
		t.Errorf("expected case PERMISSION_DENIED in body, got: %q", got.Body)
	}
	if !strings.Contains(got.Signature, "values") {
		t.Errorf("expected signature to mark values form, got: %q", got.Signature)
	}
	foundMessage := false
	for _, m := range got.Methods {
		if m.Name == "message" {
			foundMessage = true
			break
		}
	}
	if !foundMessage {
		t.Errorf("expected method 'message' in Methods, got: %+v", got.Methods)
	}
	// consumeStructBody-by-brace-depth gets confused if the cases-block
	// `} {` (methods opener on the same closing-brace line) is mis-
	// tracked, and the method body folds into Body. The runtime/errors
	// fixture has the methods block on a separate line so the easy
	// path works, but the proposal §85 example puts `} {` on one line
	// — the bug would have the method body leak into Body.
	if strings.Contains(got.Body, "fn message") || strings.Contains(got.Body, "return byte[](e)") {
		t.Errorf("method body bled into Body: %q", got.Body)
	}
}
