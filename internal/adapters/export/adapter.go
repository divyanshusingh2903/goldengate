package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/model"
)

// Head is the cheap metadata of one conversation.
type Head struct {
	ID       string
	Project  string // virtual project key, e.g. "claude-ai:Chats"
	Title    string
	Created  time.Time
	Updated  time.Time
	Messages int
}

// Parser knows one product's export layout. Implementations must treat every
// byte as hostile: decode defensively, return errors for unknown shapes.
type Parser interface {
	Source() string
	// Files names the archive entries holding conversations (exact names).
	Files(a *Archive) []string
	// Prepare reads side files needed to interpret conversations (project names).
	Prepare(a *Archive) (any, error)
	// Head extracts metadata from one conversation element.
	Head(raw []byte, ctx any) (Head, error)
	// Load turns one conversation element into a session.
	Load(raw []byte, ctx any, h Head) (*model.RawSession, error)
}

// MaxCacheBytes bounds how much decompressed export data one Adapter holds in memory.
const MaxCacheBytes = 1 << 30

type element struct {
	head Head
	raw  []byte // slice of the entry's bytes
}

type index struct {
	elems   map[string]element
	ctx     any
	bytes   int64
	skipped int
}

// Adapter serves one or more export archives of a single product as sessions.
type Adapter struct {
	parser Parser
	paths  []string
	lim    Limits

	mu    sync.Mutex
	cache map[string]*index
	total int64
}

// New builds an adapter over the given archive paths.
func New(p Parser, paths []string, lim Limits) *Adapter {
	return &Adapter{parser: p, paths: append([]string(nil), paths...), lim: lim, cache: map[string]*index{}}
}

// Name implements adapters.Adapter.
func (a *Adapter) Name() string { return a.parser.Source() }

// Detect implements adapters.Adapter: "installed" means at least one imported archive is still there.
func (a *Adapter) Detect(ctx context.Context) (adapters.Detection, error) {
	if err := ctx.Err(); err != nil {
		return adapters.Detection{}, err
	}
	d := adapters.Detection{Root: fmt.Sprintf("%d imported archive(s)", len(a.paths))}
	if len(a.paths) == 0 {
		d.Notes = append(d.Notes, "nothing imported yet (see `goldengate import-"+a.Name()+"`)")
		return d, nil
	}
	for _, p := range a.paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			d.Installed = true
		} else {
			d.Notes = append(d.Notes, "imported archive is missing: "+p)
		}
	}
	return d, nil
}

// load opens and indexes one archive, once.
func (a *Adapter) load(path string) (*index, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ix, ok := a.cache[path]; ok {
		return ix, nil
	}
	arc, err := Open(path, a.lim)
	if err != nil {
		return nil, err
	}
	defer func() { _ = arc.Close() }()
	ctx, err := a.parser.Prepare(arc)
	if err != nil {
		return nil, err
	}
	ix := &index{elems: map[string]element{}, ctx: ctx}
	files := a.parser.Files(arc)
	if len(files) == 0 {
		return nil, fmt.Errorf("no conversation file found in the archive (is this a %s export?)", a.Name())
	}
	for _, name := range files {
		data, err := arc.Read(name)
		if err != nil {
			return nil, err
		}
		if a.total+ix.bytes+int64(len(data)) > MaxCacheBytes {
			return nil, fmt.Errorf("exports are larger than the %d byte in-memory limit", MaxCacheBytes)
		}
		ix.bytes += int64(len(data))
		elems, err := splitArray(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, raw := range elems {
			h, err := a.parser.Head(raw, ctx)
			if err != nil || h.ID == "" {
				ix.skipped++
				continue
			}
			if old, dup := ix.elems[h.ID]; !dup || h.Updated.After(old.head.Updated) {
				ix.elems[h.ID] = element{head: h, raw: raw}
			}
		}
	}
	a.total += ix.bytes
	a.cache[path] = ix
	return ix, nil
}

// Summary describes an archive for the import command.
type Summary struct {
	Conversations int
	Skipped       int
	Buckets       map[string]int
}

// Probe validates and indexes one archive without registering it.
func (a *Adapter) Probe(path string) (Summary, error) {
	ix, err := a.load(path)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Conversations: len(ix.elems), Skipped: ix.skipped, Buckets: map[string]int{}}
	for _, e := range ix.elems {
		s.Buckets[e.head.Project]++
	}
	return s, nil
}

// ListSessions implements adapters.Adapter. A conversation present in several
// exports is listed once, from the export that saw it last.
func (a *Adapter) ListSessions(ctx context.Context, opt adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(yield func(model.SessionRef, error) bool) {
		best := map[string]model.SessionRef{}
		var order []string
		for _, p := range a.paths {
			if ctx.Err() != nil {
				yield(model.SessionRef{}, ctx.Err())
				return
			}
			ix, err := a.load(p)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					err = errors.New("imported archive is missing")
				}
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: p, Reason: err.Error()}) {
					return
				}
				continue
			}
			if ix.skipped > 0 {
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: p, Reason: fmt.Sprintf("%d conversation(s) in an unknown shape were skipped", ix.skipped)}) {
					return
				}
			}
			for id, e := range ix.elems {
				ref := model.SessionRef{Source: a.Name(), ID: id, ProjectKey: e.head.Project, Path: p,
					StartedAt: e.head.Created, UpdatedAt: e.head.Updated, SizeBytes: int64(e.head.Messages)}
				if old, dup := best[id]; !dup {
					order = append(order, id)
					best[id] = ref
				} else if !ref.UpdatedAt.Before(old.UpdatedAt) {
					best[id] = ref
				}
			}
		}
		sort.Strings(order)
		for _, id := range order {
			ref := best[id]
			if !opt.Since.IsZero() && ref.UpdatedAt.Before(opt.Since) {
				continue
			}
			if !yield(ref, nil) {
				return
			}
		}
	}
}

// Load implements adapters.Adapter.
func (a *Adapter) Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ix, err := a.load(ref.Path)
	if err != nil {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: err.Error()}
	}
	e, ok := ix.elems[ref.ID]
	if !ok {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "conversation " + ref.ID + " is no longer in the archive"}
	}
	s, err := a.parser.Load(e.raw, ix.ctx, e.head)
	if err != nil {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "conversation " + ref.ID + ": " + err.Error()}
	}
	s.Ref = ref
	if s.ToolsUsed == nil {
		s.ToolsUsed = map[string]int{}
	}
	return s, nil
}

// splitArray returns the elements of a top-level JSON array as sub-slices of data.
func splitArray(data []byte) ([][]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, errors.New("expected a JSON array of conversations (unknown shape)")
	}
	var out [][]byte
	for dec.More() {
		start := dec.InputOffset()
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return out, fmt.Errorf("damaged JSON after %d conversations: %w", len(out), err)
		}
		end := dec.InputOffset()
		out = append(out, bytes.TrimLeft(data[start:end], " \t\r\n,"))
	}
	return out, nil
}

// Bucket turns a product's project/GPT name into the part of a virtual project
// key after "<source>:". Characters that would act as glob syntax or confuse
// display are replaced; an empty name becomes fallback.
func Bucket(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune("*?[]{}", r):
			return '_'
		case unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200B || r == 0xFEFF:
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if rs := []rune(name); len(rs) > 60 {
		name = string(rs[:60])
	}
	if name == "" {
		return fallback
	}
	return name
}
