package packer

import "testing"

func TestPkgBasename(t *testing.T) {
	f := func(p Pkg, wantBasename string) {
		t.Helper()
		got := p.Basename()
		if got != wantBasename {
			t.Errorf("pkg.Basename got %q, want %q", got, wantBasename)
		}
	}

	f(Pkg{
		Target: "target-name",
	}, "target-name")

	f(Pkg{
		ImportPath: "example.com/import/path",
		Target:     "target-name",
	}, "target-name")

	f(Pkg{
		ImportPath: "example.com/import/path",
		Target:     "",
	}, "path")
	f(Pkg{
		ImportPath: "example.com/import/path/v2",
		Target:     "",
	}, "path")
}
