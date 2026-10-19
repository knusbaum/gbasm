package main

import (
	"strings"
	"testing"
)

func TestSliceOwnershipTransfers(t *testing.T) {
	tests := []struct {
		name, body, wantErr string
	}{

		{name: "field self assignment", body: `type B struct{buf owned []mut byte}
 fn trim(b *mut B,n i64){b.buf=b.buf[:n]}`},
		{name: "owned value field return", body: `type B struct{buf owned []mut byte}
 fn build() owned []mut byte{
  b owned B:=B{buf:make([]mut byte,8)}
  out owned []mut byte:=b.buf[:3]
  dispose(b)
  return out
 }`},
		{name: "field argument with restoration", body: `type B struct{buf owned []mut byte}
 fn take(s owned []mut byte){free(s)}
 fn use(b *mut B){take(b.buf[:2]);b.buf=make([]mut byte,8)}`},
		{name: "readonly field prefix", body: `type B struct{buf owned []mut byte}
 fn bad(b *B) owned []byte{return b.buf[:2]}`, wantErr: "zero-offset prefix"},
		{name: "borrowed field restoration required", body: `type B struct{buf owned []mut byte}
 fn bad(b *mut B) owned []mut byte{return b.buf[:2]}`, wantErr: "borrowed owned field"},
		{name: "element-owned field prefix", body: `type B struct{buf owned []owned i64}
 fn bad(b *mut B){b.buf=b.buf[:2]}`, wantErr: "element-owned slice"},
		{name: "direct return", body: `fn build() owned []byte { s := make([]mut byte, 8); return s }`},
		{name: "prefix return", body: `fn build() owned []byte { s := make([]mut byte, 8); return s[:3] }`},
		{name: "explicit zero", body: `fn build() owned []byte { s := make([]mut byte, 8); return s[0:3] }`},
		{name: "constant zero expression", body: `fn build() owned []byte { s := make([]mut byte, 8); return s[2-2:3] }`},
		{name: "nested prefixes", body: `fn build() owned []byte { s := make([]mut byte, 8); return s[:6][:3] }`},
		{name: "owning parameter", body: `fn trim(s owned []mut byte) owned []byte { return s[:3] }`},
		{name: "owning pointer to array", body: `fn build() owned []byte { p := alloc([8]byte); return (*p)[:3] }`},
		{name: "initializer", body: `fn build() owned []byte {
   s := make([]mut byte, 8)
   t owned []byte := s[:3]
   return t
  }`},
		{name: "assignment", body: `fn build() owned []mut byte {
   s := make([]mut byte, 8)
   var t := make([]mut byte, 1)
   free(t)
   t = s[:3]
   return t
  }`},
		{name: "self assignment", body: `fn build() owned []mut byte {
   var s := make([]mut byte, 8)
   s = s[:3]
   return s
  }`},
		{name: "argument", body: `fn take(s owned []byte) { free(s) }
   fn build() { s := make([]mut byte, 8); take(s[:3]) }`},
		{name: "multiple return", body: `fn build() owned []byte, i64 {
   s := make([]mut byte, 8)
   return s[:3], 3
  }`},
		{name: "destructured owner tracks borrows", body: `fn build() owned []mut byte, i64 {
   s := make([]mut byte, 8)
   return s[:3], 3
  }
  fn use() byte {
   s, n := build()
   v := s[:]
   free(s)
   return v[n-1]
  }`, wantErr: "allocation was freed"},
		{name: "borrow survives local transfer", body: `fn use() byte {
   s := make([]mut byte, 8)
   v := s[:]
   t owned []byte := s[:3]
   result := v[0]
   free(t)
   return result
  }`},
		{name: "borrow dies with transferred owner", body: `fn use() byte {
   s := make([]mut byte, 8)
   v := s[:]
   t owned []byte := s[:3]
   free(t)
   return v[0]
  }`, wantErr: "allocation was freed"},
		{name: "helper preserves resource dependency", body: `fn trim(s owned []mut byte) owned []byte { return s[:3] }
  fn use() byte {
   s := make([]mut byte, 8)
   v := s[:]
   t := trim(s)
   free(t)
   return v[0]
  }`, wantErr: "allocation was freed"},
		{name: "owned struct field", body: `type Box struct { text owned []byte }
  fn build() owned Box {
   s := make([]mut byte, 8)
   return Box{text: s[:3]}
  }`},
		{name: "element obligations cannot be truncated", body: `fn bad(s owned []owned i64) owned []owned i64 {
   return s[:1]
  }`, wantErr: "element-owned slice"},
		{name: "element ownership cannot be dropped on return", body: `fn bad(s owned []owned i64) owned []i64 {
   return s
  }`, wantErr: "Cannot return"},
		{name: "multiple owning helper return", body: `fn trim(s owned []mut byte) owned []byte, i64 {
   return s[:3], 3
  }
  fn use() byte {
   s := make([]mut byte, 8)
   v := s[:]
   t, n := trim(s)
   free(t)
   return v[n-1]
  }`, wantErr: "allocation was freed"},
		{name: "offset initializer", body: `fn use() {
   s := make([]mut byte, 8)
   t owned []byte := s[1:]
   free(t)
  }`, wantErr: "zero-offset prefix"},
		{name: "borrowed owning argument", body: `fn take(s owned []byte) { free(s) }
  fn use(s []byte) { take(s) }`, wantErr: "source has no owned backing"},
		{name: "nonzero offset", body: `fn build() owned []byte {
   s := make([]mut byte, 8)
   return s[1:]
  }`, wantErr: "zero-offset prefix"},
		{name: "unknown offset", body: `fn build(n i64) owned []byte {
   s := make([]mut byte, 8)
   return s[n:]
  }`, wantErr: "zero-offset prefix"},
		{name: "nested offset", body: `fn build() owned []byte {
   s := make([]mut byte, 8)
   return s[1:][:3]
  }`, wantErr: "zero-offset prefix"},
		{name: "borrowed prefix", body: `fn build(s []byte) owned []byte {
   return s[:3]
  }`, wantErr: "zero-offset prefix"},
		{name: "borrowed direct return", body: `fn build(s []byte) owned []byte {
   return s
  }`, wantErr: "source has no owned backing"},
		{name: "borrowed multiple return", body: `fn build(s []byte) owned []byte, i64 {
   return s, 3
  }`, wantErr: "source has no owned backing"},
		{name: "ordinary local array", body: `fn build() owned []byte {
   var s [8]byte
   return s[:3]
  }`, wantErr: "zero-offset prefix"},
		{name: "owned local array is not backing owner", body: `fn build() owned []byte {
   var s owned [8]byte
   return s[:3]
  }`, wantErr: "zero-offset prefix"},
		{name: "borrow alias cannot claim ownership", body: `fn build() owned []byte {
   s := make([]mut byte, 8)
   v := s[:]
   return v
  }`, wantErr: "source has no owned backing"},
		{name: "old owner is moved", body: `fn use() {
   s := make([]mut byte, 8)
   t owned []byte := s[:3]
   free(s)
   free(t)
  }`, wantErr: "moved"},
		{name: "two return slots cannot own same resource", body: `fn build() owned []byte, owned []byte {
   s := make([]mut byte, 8)
   return s[:3], s[:4]
  }`, wantErr: "moved"},
		{name: "ordinary slice remains borrowed", body: `fn use() {
   s := make([]mut byte, 8)
   v := s[:3]
   free(v)
   free(s)
  }`, wantErr: "free requires an owned"},
		{name: "borrow return leaves obligation unresolved", body: `fn build() []byte {
   s := make([]mut byte, 8)
   return s[:3]
  }`, wantErr: "without being consumed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceForTest(matrixSource(tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
