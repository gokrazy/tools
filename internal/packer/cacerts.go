package packer

import (
	"encoding/pem"
	"os"
	"path/filepath"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

func (pack *Pack) findSystemCertsPEM() (string, error) {
	log := pack.Env.Logger()

	var source string
	defer func() {
		log.Printf("Loading system CA certificates from %s", source)
	}()

	// On Linux, we can copy the operating system’s certificate store.
	// certFiles is defined in cacerts_linux.go (or defined as empty in
	// cacertsstub.go on non-Linux):
	for _, fn := range certFiles {
		b, err := os.ReadFile(fn)
		if err != nil {
			continue
		}
		source = fn
		return string(b), nil
	}

	// Perhaps the user arranged for a fallback certificate store:
	home, err := homedir()
	if err != nil {
		return "", err
	}
	fallback := filepath.Join(home, ".config", "gokrazy", "cacert.pem")
	if b, err := os.ReadFile(fallback); err == nil {
		source = fallback
		return string(b), nil
	}

	// Fall back to the x/crypto fallback bundle root certificates:
	source = "bundled x509roots/fallback/bundle"
	return xrf(), nil
}

func xrf() string {
	var certs []byte
	for c := range bundle.Roots() {
		certs = append(certs, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: c.Certificate,
		})...)
	}
	return string(certs)
}
