package shellcmd

import (
	"reflect"
	"testing"
)

func names(invs []Invocation) []string {
	var out []string
	for _, i := range invs {
		out = append(out, i.Name())
	}
	return out
}

func TestParse(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"chain", `cd x && git add -A && git commit -m "m"`, []string{"cd", "git", "git"}},
		{"env prefix", `FOO=bar go test ./...`, []string{"go"}},
		{"sudo wrapper", `sudo apt install x`, []string{"apt"}},
		{"pipe and semicolon", `a | b; c || d`, []string{"a", "b", "c", "d"}},
		{"quoted text is not a command", `echo "git commit -m x && gh pr merge 1"`, []string{"echo"}},
		{"single quotes", `echo 'git commit'`, []string{"echo"}},
		{"heredoc body dropped", "cat > f <<'EOF'\nif x; then\n  git commit -m y\nfi\nEOF\ngo build", []string{"cat", "go"}},
		{"heredoc dash", "cat <<-EOT\n\tgh pr create\n\tEOT\nls", []string{"cat", "ls"}},
		{"unterminated heredoc", "cat <<EOF\ngit commit", []string{"cat"}},
		{"command substitution", "git commit -m \"$(cat <<'EOF'\nmsg gh pr create\nEOF\n)\"", []string{"git"}},
		{"backticks", "echo `git commit`", []string{"echo"}},
		{"keywords", "if true; then git status; fi", []string{"true", "git"}},
		{"for loop dropped", "for i in 1 2; do echo $i; done", []string{"echo"}},
		{"comment", "ls # git commit", []string{"ls"}},
		{"continuation", "git \\\n commit -m x", []string{"git"}},
	}
	for _, c := range cases {
		if got := names(Parse(c.in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestQuotedWordsKeepContent(t *testing.T) {
	invs := Parse(`gh pr create --title "Add widget" --body 'x y'`)
	if len(invs) != 1 || !reflect.DeepEqual(invs[0].Words, []string{"gh", "pr", "create", "--title", "Add widget", "--body", "x y"}) {
		t.Fatalf("%+v", invs)
	}
	if v, ok := Flag(invs[0].Words[3:], "title", "t"); !ok || v != "Add widget" {
		t.Fatalf("flag: %q %v", v, ok)
	}
	if v, ok := Flag([]string{"--title=A B"}, "title", "t"); !ok || v != "A B" {
		t.Fatalf("flag=: %q %v", v, ok)
	}
	if !Has([]string{"--dry-run"}, "--dry-run") || Has(nil, "x") {
		t.Fatal("Has")
	}
}

func TestGitSubcommand(t *testing.T) {
	for in, want := range map[string]string{
		"git commit -m x":                      "commit",
		"git -C /repo commit -m x":             "commit",
		"git -c user.name=x --no-pager commit": "commit",
		"git status":                           "status",
	} {
		sub, _, ok := GitSubcommand(Parse(in)[0])
		if !ok || sub != want {
			t.Errorf("%q: %q %v", in, sub, ok)
		}
	}
	if _, _, ok := GitSubcommand(Parse("echo git commit")[0]); ok {
		t.Error("echo is not git")
	}
}

func FuzzParse(f *testing.F) {
	f.Add("a && b <<EOF\nx\nEOF")
	f.Add(`"`)
	f.Add("$(")
	f.Fuzz(func(t *testing.T, s string) { Parse(s) })
}
