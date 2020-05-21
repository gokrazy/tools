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

func findConfig(gokrazyConfigDir, gokrazyHttpPasswordTxt, fn, hostname string) (string, error) {
	if hostname == "" {
		return gokrazyHttpPasswordTxt, nil
	}

	pwPath := filepath.Join(gokrazyConfigDir, "hosts", hostname, fn)
	_, err := os.Stat(pwPath)
	switch {
	case err == nil:
		return pwPath, nil // host-specific config exists

	case os.IsNotExist(err):
		return gokrazyHttpPasswordTxt, nil // fallback

	default:
		return "", err
	}
}

func ensurePasswordFileExists(hostname, defaultPassword string) (password, path string, err error) {
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	// Typically ~/.config/gokrazy
	gokrazyConfigDir := filepath.Join(userConfigDir, "gokrazy")
	gokrazyHttpPasswordTxt := filepath.Join(gokrazyConfigDir, "http-password.txt")

	{
		path, err := findConfig(gokrazyConfigDir, gokrazyHttpPasswordTxt, "http-password.txt", hostname)
		if err != nil {
			return "", "", err
		}
		if pwb, err := ioutil.ReadFile(path); err == nil {
			return strings.TrimSpace(string(pwb)), path, nil
		}
	}

	pw := defaultPassword
	if pw == "" {
		pw, err = randomPassword(20)
		if err != nil {
			return "", "", err
		}
	}

	if err := os.MkdirAll(gokrazyConfigDir, 0700); err != nil {
		return "", "", err
	}

	if err := ioutil.WriteFile(gokrazyHttpPasswordTxt, []byte(pw+"\n"), 0600); err != nil {
		return "", "", err
	}

	return pw, gokrazyHttpPasswordTxt, nil
}
