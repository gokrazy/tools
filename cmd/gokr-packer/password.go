package main

import (
	"crypto/rand"
	"errors"
	"io/ioutil"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"strings"
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

func ensurePasswordFileExists(defaultPassword string) (password, path string, err error) {
	home, err := homedir()
	if err != nil {
		return "", "", err
	}

	pwPath := filepath.Join(home, ".config", "gokrazy", "http-password.txt")
	pwb, err := ioutil.ReadFile(pwPath)
	if err == nil {
		return strings.TrimSpace(string(pwb)), pwPath, nil
	}

	pw := defaultPassword
	if pw == "" {
		pw, err = randomPassword(20)
		if err != nil {
			return "", "", err
		}
	}

	if err := os.MkdirAll(filepath.Dir(pwPath), 0700); err != nil {
		return "", "", err
	}

	if err := ioutil.WriteFile(pwPath, []byte(pw+"\n"), 0600); err != nil {
		return "", "", err
	}

	return pw, pwPath, nil
}
