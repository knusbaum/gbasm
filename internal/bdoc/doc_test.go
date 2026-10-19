package bdoc

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFencedDocumentation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "example")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	source := "// Example package.\n//\n// ```boson\n// fn main() {\n//     // <script>alert(1)</script> & text\n//\n//     return\n// }\n// ```\n//\n// ## After the example\npackage example\n\n// Thing has an example.\n//\n// ```text\n//     nested example\n// ```\npub type Thing struct { x i64 }\n"
	if err := os.WriteFile(filepath.Join(dir, "example.bos"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanPackage(dir, "example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scan.DocComment, "    // <script>") {
		t.Fatal("scanner lost code indentation")
	}
	r := httptest.NewRecorder()
	Handler(Options{BosonPath: root}).ServeHTTP(r, httptest.NewRequest("GET", "/pkg/example/", nil))
	if r.Code != 200 {
		t.Fatalf("status %d", r.Code)
	}
	body := r.Body.String()
	for _, want := range []string{"<pre><code>fn main() {\n    // &lt;script&gt;alert(1)&lt;/script&gt; &amp; text\n\n    return\n}</code></pre>", "<h2>After the example</h2>", "<pre><code>    nested example</code></pre>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing rendered content %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("unescaped source in documentation")
	}
}
