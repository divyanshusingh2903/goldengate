// Package shellcmd splits a shell command line into simple invocations so
// detectors only look at commands that actually run: text inside heredoc
// bodies, quoted strings and command substitutions is never mistaken for a
// command. It is a heuristic tokenizer, not a shell: it under-detects rather
// than over-detects.
package shellcmd

import (
	"regexp"
	"strings"
)

// Invocation is one simple command: its words after quote removal.
type Invocation struct{ Words []string }

// Name is the program name (first word), or "".
func (i Invocation) Name() string {
	if len(i.Words) == 0 {
		return ""
	}
	return i.Words[0]
}

var reHeredoc = regexp.MustCompile(`<<-?\s*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|\\?([A-Za-z_][A-Za-z0-9_]*))`)

// StripHeredocs removes heredoc bodies (and their terminator lines).
func StripHeredocs(cmd string) string {
	lines := strings.Split(cmd, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		out = append(out, line)
		for _, m := range reHeredoc.FindAllStringSubmatch(line, -1) {
			tag := m[1] + m[2] + m[3]
			dash := strings.Contains(m[0], "<<-")
			j := i + 1
			for ; j < len(lines); j++ {
				l := lines[j]
				if dash {
					l = strings.TrimLeft(l, "\t")
				}
				if l == tag {
					break
				}
			}
			i = j // skip body and terminator (or to the end if unterminated)
		}
	}
	return strings.Join(out, "\n")
}

var skipWords = map[string]bool{"sudo": true, "env": true, "time": true, "nohup": true, "command": true, "builtin": true, "exec": true}
var leadWords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true, "done": true, "while": true, "until": true, "!": true, "{": true, "}": true}
var dropWhole = map[string]bool{"for": true, "case": true, "esac": true, "function": true, "select": true}
var reAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// Parse returns the simple commands in cmd, in order.
func Parse(cmd string) []Invocation {
	cmd = StripHeredocs(cmd)
	var invs []Invocation
	var words []string
	var cur strings.Builder
	inWord := false

	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if inv, ok := clean(words); ok {
			invs = append(invs, inv)
		}
		words = nil
	}

	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\':
			if i+1 < len(rs) {
				if rs[i+1] != '\n' { // line continuation
					cur.WriteRune(rs[i+1])
					inWord = true
				}
				i++
			}
		case c == '\'':
			inWord = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				switch {
				case rs[i] == '\\' && i+1 < len(rs):
					i++
					cur.WriteRune(rs[i])
				case rs[i] == '$' && i+1 < len(rs) && rs[i+1] == '(':
					i = skipSubst(rs, i+1)
				case rs[i] == '`':
					i = skipBacktick(rs, i)
				default:
					cur.WriteRune(rs[i])
				}
			}
		case c == '$' && i+1 < len(rs) && rs[i+1] == '(':
			inWord = true
			i = skipSubst(rs, i+1)
		case c == '`':
			inWord = true
			i = skipBacktick(rs, i)
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		case c == '\n' || c == ';':
			endCmd()
		case c == '&' || c == '|':
			endCmd()
			if i+1 < len(rs) && rs[i+1] == c {
				i++
			}
		case c == '(' || c == ')':
			endCmd()
		case c == '#' && !inWord:
			for i < len(rs) && rs[i] != '\n' { // comment
				i++
			}
			endCmd()
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	endCmd()
	return invs
}

// skipSubst returns the index of the ")" closing the substitution that opens
// at rs[open] == '('.
func skipSubst(rs []rune, open int) int {
	depth := 0
	for i := open; i < len(rs); i++ {
		switch rs[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(rs)
}

func skipBacktick(rs []rune, open int) int {
	for i := open + 1; i < len(rs); i++ {
		if rs[i] == '`' {
			return i
		}
	}
	return len(rs)
}

func clean(words []string) (Invocation, bool) {
	for len(words) > 0 {
		w := words[0]
		switch {
		case reAssign.MatchString(w), skipWords[w], leadWords[w]:
			words = words[1:]
			continue
		case dropWhole[w]:
			return Invocation{}, false
		}
		break
	}
	if len(words) == 0 {
		return Invocation{}, false
	}
	return Invocation{Words: words}, true
}

// GitSubcommand returns the git subcommand and its arguments, skipping
// global options such as -C <dir> and -c <k=v>. ok is false when the
// invocation is not git.
func GitSubcommand(inv Invocation) (sub string, args []string, ok bool) {
	if inv.Name() != "git" {
		return "", nil, false
	}
	w := inv.Words[1:]
	for len(w) > 0 && strings.HasPrefix(w[0], "-") {
		switch w[0] {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			if len(w) < 2 {
				return "", nil, false
			}
			w = w[2:]
		default:
			w = w[1:]
		}
	}
	if len(w) == 0 {
		return "", nil, false
	}
	return w[0], w[1:], true
}

// Flag returns the value of --name or -n (as "--name value", "--name=value",
// "-n value"). ok is false when the flag is absent.
func Flag(args []string, long, short string) (string, bool) {
	for i, a := range args {
		switch {
		case a == "--"+long || (short != "" && a == "-"+short):
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		case strings.HasPrefix(a, "--"+long+"="):
			return a[len(long)+3:], true
		}
	}
	return "", false
}

// Has reports whether args contains the exact flag.
func Has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
