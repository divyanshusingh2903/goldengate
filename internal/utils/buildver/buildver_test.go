package buildver

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct{ name, v, module, want string }{
		{"installed release", "dev", "v1.1.2", "v1.1.2"},
		{"ldflags wins", "1.2.0", "v1.1.2", "1.2.0"},
		{"local build", "dev", "(devel)", "dev"},
		{"no module version", "dev", "", "dev"},
	}
	for _, c := range cases {
		if got := resolve(c.v, c.module); got != c.want {
			t.Errorf("%s: resolve(%q, %q) = %q, want %q", c.name, c.v, c.module, got, c.want)
		}
	}
}

func TestResolveKeepsNonDev(t *testing.T) {
	if got := Resolve("9.9.9"); got != "9.9.9" {
		t.Errorf("Resolve(9.9.9) = %q", got)
	}
}
