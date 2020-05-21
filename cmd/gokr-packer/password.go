package main

import (
	"crypto/rand"
	"errors"
	"io/ioutil"
	"math/big"
	"os"
	"os/user"
	"path/filepath"

	"github.com/gokrazy/internal/config"
)

func randomChar() (byte, error) {
	charset := "abcdefghijklmnopqrstuvwxyz" +
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"0123456789"

	bn, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
	if err != nil {
		return 0, err
	}
	n := byte(bn.Int64())
	return charset[n], nil
}

func randomPassword(n int) (string, error) {
	pw := make([]byte, n)
	for i := 0; i < n; i++ {
		c, err := randomChar()
		if err != nil {
			return "", err
		}
		pw[i] = c
	}
	return string(pw), nil
}

func homedir() (string, error) {
	if u, err := user.Current(); err == nil {
		return u.HomeDir, nil
	}
	if home := os.Getenv("HOME"); home != "" {
		return home, nil
	}
	return "", errors.New("$HOME is unset and user.Current failed")
}

func ensurePasswordFileExists(hostname, defaultPassword string) (password string, err error) {
	const configBaseName = "http-password.txt"
	if pwb, err := config.HostnameSpecific(hostname).ReadFile(configBaseName); err == nil {
		return pwb, nil
	}

	pw := defaultPassword
	if pw == "" {
		pw, err = randomPassword(20)
		if err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(config.Gokrazy(), 0700); err != nil {
		return "", err
	}

	// Save the password without a trailing \n so that xclip can be used to
	// copy&paste the password into a browser:
	//   % xclip < ~/.config/gokrazy/http-password.txt
	if err := ioutil.WriteFile(filepath.Join(config.Gokrazy(), configBaseName), []byte(pw), 0600); err != nil {
		return "", err
	}

	return pw, nil
}
