package main

// FIXME this should be replaced with the logic from go1.8/src/crypto/x509/root_darwin.go

var certFiles = []string{
	"/usr/local/etc/openssl/cert.pem", // macOS Homebrew
}
