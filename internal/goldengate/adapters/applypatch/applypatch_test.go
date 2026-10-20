package applypatch

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	in := "*** Begin Patch\r\n*** Add File: a/b.go\r\n+package b\r\n*** Update File: c.go\n@@\n-x\n+y\n*** Delete File: old.go\n*** Update File: \n*** End Patch"
	got := Parse(in)
	want := []File{{Add, "a/b.go"}, {Update, "c.go"}, {Delete, "old.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if len(Parse("no patch here")) != 0 || len(Parse("")) != 0 {
		t.Error("non-patches yield nothing")
	}
	// contents of added files that look like headers are indented with '+', so they are not headers
	if got := Parse("*** Add File: x\n+*** Add File: injected"); len(got) != 1 {
		t.Errorf("content lines must not count: %+v", got)
	}
}
