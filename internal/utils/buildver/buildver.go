// Package buildver finds the version a binary was built from.
package buildver

import "runtime/debug"

// Resolve returns v unless it is still the placeholder "dev", in which case
// it returns the module version the Go toolchain embedded (what `go install
// module@v1.2.3` records). Release builds that set v with -ldflags -X keep
// their value; local builds, whose module version is "(devel)" or empty, stay
// "dev".
func Resolve(v string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	return resolve(v, info.Main.Version)
}

func resolve(v, module string) string {
	if v != "dev" || module == "" || module == "(devel)" {
		return v
	}
	return module
}
