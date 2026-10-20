// Package export reads data-export zips (claude.ai, ChatGPT) without ever
// extracting them to disk. Archives are untrusted input: every entry name and
// size is checked up front (zip-slip, symlinks, bombs), only a few known file
// names are ever opened, and decompression is bounded no matter what the
// archive claims about itself.
package export

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Limits bound what an archive may do to this process.
type Limits struct {
	MaxArchiveBytes int64 // size of the zip file itself
	MaxEntries      int
	MaxEntryBytes   int64 // decompressed size of any single entry we read
	MaxTotalBytes   int64 // declared decompressed size of the whole archive
	MaxRatio        int64 // reject entries compressing better than this (bombs)
}

// DefaultLimits suit real exports (tens to a few hundred MB) while rejecting bombs.
func DefaultLimits() Limits {
	return Limits{MaxArchiveBytes: 4 << 30, MaxEntries: 50_000, MaxEntryBytes: 512 << 20, MaxTotalBytes: 8 << 30, MaxRatio: 200}
}

// unsigned converts a limit to uint64, treating negative limits as zero.
func unsigned(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// Archive is a validated, read-only zip.
type Archive struct {
	zr    *zip.ReadCloser
	byKey map[string]*zip.File
	lim   Limits
}

// Open validates the zip at p. It refuses symlinked or non-regular files.
func Open(p string, lim Limits) (*Archive, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if st.Size() > lim.MaxArchiveBytes {
		return nil, fmt.Errorf("archive is %d bytes, over the %d byte limit", st.Size(), lim.MaxArchiveBytes)
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("not a readable zip: %w", err)
	}
	a := &Archive{zr: zr, byKey: map[string]*zip.File{}, lim: lim}
	if err := a.validate(); err != nil {
		_ = zr.Close()
		return nil, err
	}
	return a, nil
}

// Close releases the file.
func (a *Archive) Close() error { return a.zr.Close() }

// SafeName reports whether an entry name is a plain relative path: no
// traversal, no absolute path, no drive letters or backslashes, no NULs.
func SafeName(n string) bool {
	if n == "" || strings.ContainsAny(n, "\\:") || strings.HasPrefix(n, "/") {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(strings.TrimSuffix(n, "/"), "/") {
		if seg == ".." || seg == "" && n != "" && !strings.HasSuffix(n, "/") {
			return false
		}
	}
	return path.Clean(n) == strings.TrimSuffix(n, "/")
}

func (a *Archive) validate() error {
	if len(a.zr.File) > a.lim.MaxEntries {
		return fmt.Errorf("archive has %d entries, over the %d limit", len(a.zr.File), a.lim.MaxEntries)
	}
	var total uint64
	for _, f := range a.zr.File {
		if !SafeName(f.Name) {
			return fmt.Errorf("archive entry has an unsafe name (%q): refusing the whole archive", printable(f.Name))
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a symlink: refusing the whole archive", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if _, dup := a.byKey[f.Name]; dup {
			return fmt.Errorf("archive has two entries named %q: refusing it", f.Name)
		}
		a.byKey[f.Name] = f
		total += f.UncompressedSize64
		if total > unsigned(a.lim.MaxTotalBytes) {
			return fmt.Errorf("archive would expand beyond %d bytes: refusing it", a.lim.MaxTotalBytes)
		}
		if f.UncompressedSize64 > 1<<20 && f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > unsigned(a.lim.MaxRatio) {
			return fmt.Errorf("archive entry %q is suspiciously compressible (zip bomb?): refusing it", f.Name)
		}
	}
	return nil
}

func printable(s string) string {
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// Has reports whether the archive contains a file with exactly this name.
func (a *Archive) Has(name string) bool { _, ok := a.byKey[name]; return ok }

// Names lists the files (not directories) in the archive.
func (a *Archive) Names() []string {
	out := make([]string, 0, len(a.byKey))
	for n := range a.byKey {
		out = append(out, n)
	}
	return out
}

// ErrTooLarge means an entry exceeded its limit while being read.
var ErrTooLarge = errors.New("export entry is larger than the allowed limit")

// Read returns the decompressed bytes of one exact entry name. The declared
// size is checked first, and the read itself is bounded too, because a zip can
// lie about its sizes.
func (a *Archive) Read(name string) ([]byte, error) {
	f, ok := a.byKey[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	if f.UncompressedSize64 > unsigned(a.lim.MaxEntryBytes) {
		return nil, fmt.Errorf("%s: %w (%d bytes)", name, ErrTooLarge, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, a.lim.MaxEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if int64(len(b)) > a.lim.MaxEntryBytes {
		return nil, fmt.Errorf("%s: %w", name, ErrTooLarge)
	}
	return b, nil
}
