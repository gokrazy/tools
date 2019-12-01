package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gokrazy/internal/config"
)

func generateAndSignCert() ([]byte, *rsa.PrivateKey, error) {
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
		DNSNames:              []string{*hostname},
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
func generateAndStoreSelfSignedCertificate(hostConfigPath, certPath, keyPath string) error {
	fmt.Println("Generating new self-signed certificate...")
	// Generate
	if err := os.MkdirAll(string(hostConfigPath), 0755); err != nil {
		return err
	}
	cert, priv, err := generateAndSignCert()
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

func getCertificate() (string, string, error) {
	hostConfigPath := config.HostnameSpecific(*hostname)
	var certPath, keyPath string
	switch *useTLS {
	case "self-signed":
		certPath = filepath.Join(string(hostConfigPath), "cert.pem")
		keyPath = filepath.Join(string(hostConfigPath), "key.pem")
		gen := false
		exist := true
		if _, err := os.Stat(certPath); os.IsNotExist(err) {
			gen = true
			exist = false
		}
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			gen = true
			exist = false
		}
		if exist {
			// TODO: Check validity dates of existing certificate
		}
		if gen {
			if err := generateAndStoreSelfSignedCertificate(string(hostConfigPath), certPath, keyPath); err != nil {
				return "", "", err
			}
		}
	case "":
		return "", "", nil
	default:
		parts := strings.Split(*useTLS, ",")
		certPath = parts[0]
		if len(parts) > 1 {
			keyPath = parts[1]
		} else {
			return "", "", fmt.Errorf("no private key supplied")
		}
		// TODO: Check validity
	}
	return certPath, keyPath, nil
}

func getCertificateFromFile(certPath string) (*x509.Certificate, error) {
	reader, err := ioutil.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(reader)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

func getCertificateFingerprintSHA1(certificate *x509.Certificate) [sha1.Size]byte {
	return sha1.Sum(certificate.Raw)
}
