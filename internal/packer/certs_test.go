package packer

import (
	"os"
	"path"
	"testing"

	"github.com/gokrazy/tools/internal/config"
)

func Test_validateCertificate(t *testing.T) {
	createTemp, cleanup := newTempFileStore(t)
	t.Cleanup(cleanup)
	k1 := createTemp("gokrazy-cert.*.pem")
	c1 := createTemp("gokrazy-key.*.pem")
	cfg := &config.Struct{}
	if err := generateAndStoreSelfSignedCertificate(cfg, path.Dir(k1), c1, k1); err != nil {
		t.Fatalf("failed to generate self signed certificate: %v", err)
	}
	k2 := createTemp("gokrazy-cert.*.pem")
	c2 := createTemp("gokrazy-key.*.pem")
	if err := generateAndStoreSelfSignedCertificate(cfg, path.Dir(k2), c2, k2); err != nil {
		t.Fatalf("failed to generate self signed certificate: %v", err)
	}

	type args struct {
		certPath string
		keyPath  string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "valid-cert",
			args:    args{certPath: c1, keyPath: k1},
			wantErr: false,
		},
		{
			name:    "wrong-key-for-cert",
			args:    args{certPath: c1, keyPath: k2},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateCertificate(tt.args.certPath, tt.args.keyPath); (err != nil) != tt.wantErr {
				t.Errorf("validateCertificate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func newTempFileStore(t *testing.T) (createTemp func(pattern string) string, cleanup func()) {
	tmpDir, err := os.MkdirTemp("", "gokrazy-test.*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	createTemp = func(pattern string) string {
		f, err := os.CreateTemp(tmpDir, pattern)
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		return f.Name()
	}
	cleanup = func() {
		os.RemoveAll(tmpDir)
	}
	return createTemp, cleanup
}
