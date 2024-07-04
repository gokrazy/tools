package packer

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/tools/internal/pwgen"
)

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
		pw, err = pwgen.RandomPassword(20)
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
	if err := os.WriteFile(filepath.Join(config.Gokrazy(), configBaseName), []byte(pw), 0600); err != nil {
		return "", err
	}

	return pw, nil
}
