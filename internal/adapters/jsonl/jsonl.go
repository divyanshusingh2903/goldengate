// Package jsonl holds the small helpers shared by adapters that read JSONL
// session logs: bounded line reading, time parsing and text clipping.
package jsonl

import (
	"bufio"
	"path/filepath"
	"strings"
	"time"
)

// ReadLine reads one line (without the newline), keeping at most max bytes. A
// longer line is consumed fully and reported as tooLong with no data, so one
// huge event (an inlined image, say) cannot exhaust memory or stop the scan.
func ReadLine(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, isPrefix, e := br.ReadLine()
		if e != nil {
			return buf, tooLong, e
		}
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if !isPrefix {
			if tooLong {
				return nil, true, nil
			}
			return buf, false, nil
		}
	}
}

// ParseTime parses RFC 3339 timestamps; anything else yields the zero time.
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Clip truncates s to n bytes on a rune boundary.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// ClipMiddle keeps the head and tail: commit SHAs appear first, PR URLs last.
func ClipMiddle(s string, each int) string {
	if len(s) <= 2*each {
		return s
	}
	return strings.ToValidUTF8(s[:each], "") + "\n…\n" + strings.ToValidUTF8(s[len(s)-each:], "")
}

// FirstNonEmpty returns the first non-empty string.
func FirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// RelativeTo returns p relative to root; relative inputs pass through cleaned.
// ok is false for absolute paths outside root.
func RelativeTo(root, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p)), true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
