package jsonl

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadLineBoundsMemoryAndKeepsGoing(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	br := bufio.NewReaderSize(strings.NewReader("short\n"+huge+"\nafter\nlast-without-newline"), 64)
	var got []string
	var long []bool
	for {
		line, tooLong, err := ReadLine(br, 100)
		if len(line) > 0 || tooLong {
			got = append(got, string(line))
			long = append(long, tooLong)
		}
		if err != nil {
			break
		}
	}
	if len(got) != 4 || got[0] != "short" || !long[1] || got[1] != "" || got[2] != "after" || got[3] != "last-without-newline" {
		t.Fatalf("%q %v", got, long)
	}
}

func TestHelpers(t *testing.T) {
	if ParseTime("2026-01-05T09:00:00.123Z").IsZero() || !ParseTime("yesterday").IsZero() || !ParseTime("").IsZero() {
		t.Error("ParseTime")
	}
	if Clip("héllo wörld", 6) == "héllo wörld" || !strings.HasSuffix(Clip("héllo wörld", 6), "…") {
		t.Error("Clip")
	}
	if got := ClipMiddle(strings.Repeat("a", 50)+strings.Repeat("b", 50), 10); !strings.HasPrefix(got, "aaaaaaaaaa") || !strings.HasSuffix(got, "bbbbbbbbbb") || !strings.Contains(got, "…") {
		t.Errorf("ClipMiddle: %q", got)
	}
	if FirstNonEmpty("", "", "x", "y") != "x" || FirstNonEmpty() != "" {
		t.Error("FirstNonEmpty")
	}
	if r, ok := RelativeTo("/p", "/p/a/b.go"); !ok || r != "a/b.go" {
		t.Errorf("RelativeTo inside: %q %v", r, ok)
	}
	if _, ok := RelativeTo("/p", "/etc/passwd"); ok {
		t.Error("outside root")
	}
	if _, ok := RelativeTo("/p", "/p/../q"); ok {
		t.Error("escaping root")
	}
	if r, ok := RelativeTo("/p", "src/./x.go"); !ok || r != "src/x.go" {
		t.Errorf("relative passthrough: %q", r)
	}
}
