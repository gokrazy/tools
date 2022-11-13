package packer

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/internal/tlsflag"
)

func generateAndSignCert(cfg *config.Struct) ([]byte, *rsa.PrivateKey, error) {
	notBefore := time.Now()
	notAfter := notBefore.Add(2 * 365 * 24 * time.Hour)
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"gokrazy"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{cfg.Hostname},
	}
	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}
	pub := &priv.PublicKey
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, pub, priv)
	if err != nil {
		return nil, nil, err
	}
	return derBytes, priv, err
}
func generateAndStoreSelfSignedCertificate(cfg *config.Struct, hostConfigPath, certPath, keyPath string) error {
	fmt.Println("Generating new self-signed certificate...")
	// Generate
	if err := os.MkdirAll(string(hostConfigPath), 0755); err != nil {
		return err
	}
	cert, priv, err := generateAndSignCert(cfg)
	if err != nil {
		return err
	}

	// Write Certificate
	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert}); err != nil {
		return err
	}
	if err := certOut.Close(); err != nil {
		return err
	}

	// Write Key
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		//TODO: Cleanup?
		return err
	}
	if err := keyOut.Close(); err != nil {
		//TODO: Cleanup?
		return err
	}
	return nil
}

func getCertificate(cfg *config.Struct) (string, string, error) {
	certPath, keyPath, err := tlsflag.CertificatePathsFor(cfg.Hostname)
	if err != nil {
		var nycerr *tlsflag.ErrNotYetCreated
		if errors.As(err, &nycerr) {
			if err := generateAndStoreSelfSignedCertificate(cfg, nycerr.HostConfigPath, nycerr.CertPath, nycerr.KeyPath); err != nil {
				return "", "", err
			}
			return nycerr.CertPath, nycerr.KeyPath, nil
		}
	}
	if err := validateCertificate(certPath, keyPath); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func validateCertificate(certPath, keyPath string) error {
	if certPath == "" && keyPath == "" {
		return nil
	}
	_, err := tls.LoadX509KeyPair(certPath, keyPath)
	return err
}

func getCertificateFromString(certstr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certstr))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

func getCertificateFingerprintSHA1(certificate *x509.Certificate) [sha1.Size]byte {
	return sha1.Sum(certificate.Raw)
}
