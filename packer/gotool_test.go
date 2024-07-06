package packer

import "testing"

func TestPkgBasename(t *testing.T) {
	tests := []struct {
		pkg          Pkg
		wantBasename string
	}{
		{
			Pkg{
				Target: "target-name",
			},
			"target-name",
		},
		{
			Pkg{
				ImportPath: "example.com/import/path",
				Target:     "target-name",
			},
			"target-name",
		},
		{
			Pkg{
				ImportPath: "example.com/import/path",
				Target:     "",
			},
			"path",
		},
		{
			Pkg{
				ImportPath: "example.com/import/path/v2",
				Target:     "",
			},
			"path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.pkg.Target+"/"+tt.pkg.ImportPath, func(t *testing.T) {
			got := tt.pkg.Basename()
			if got != tt.wantBasename {
				t.Errorf("pkg.Basename got %q, want %q", got, tt.wantBasename)
			}
		})
	}
}
