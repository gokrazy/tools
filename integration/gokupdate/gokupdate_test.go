package gokupdate_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gokrazy/internal/config"
	"github.com/gokrazy/tools/gok"
	"github.com/gokrazy/tools/internal/packer"
)

type gokrazyTestInstance struct {
	configDir string
}

func (inst *gokrazyTestInstance) writeConfig(t *testing.T, basename, content string) {
	t.Helper()
	fn := filepath.Join(inst.configDir, basename)
	if err := os.WriteFile(fn, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeGokrazyInstance(t *testing.T) *gokrazyTestInstance {
	t.Helper()

	// Redirect os.UserConfigDir() to a temporary directory under our
	// control. gokrazy always uses a path under os.UserConfigDir().
	var configDir string
	switch runtime.GOOS {
	case "linux":
		configHomeDir := t.TempDir()
		os.Setenv("XDG_CONFIG_HOME", configHomeDir)
		// where linux looks:
		configDir = filepath.Join(configHomeDir, "gokrazy")

	case "darwin":
		homeDir := t.TempDir()
		os.Setenv("HOME", homeDir)
		// where darwin looks:
		configDir = filepath.Join(homeDir, "Library", "Application Support", "gokrazy")

	default:
		t.Fatalf("GOOS=%s unsupported", runtime.GOOS)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	return &gokrazyTestInstance{
		configDir: configDir,
	}
}

func TestGokUpdate(t *testing.T) {
	// Run this whole test in a throw-away temporary directory to not litter the
	// gokrazy/tools repository working copy.
	t.Chdir(t.TempDir())

	_ = writeGokrazyInstance(t)

	// TODO: run the gokrazy instance in a VM instead of providing a fake
	// implementation of the update protocol.
	mux := http.NewServeMux()
	mux.HandleFunc("/update/features", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/update/", func(w http.ResponseWriter, r *http.Request) {
		// accept whatever for now.
		var hash hash.Hash
		switch r.Header.Get("X-Gokrazy-Update-Hash") {
		case "crc32":
			hash = crc32.NewIEEE()
		default:
			hash = sha256.New()
		}
		if _, err := io.Copy(hash, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "%x", hash.Sum(nil))
	})
	mux.HandleFunc("/reboot", func(w http.ResponseWriter, r *http.Request) {
		// you got it, boss!
	})
	mux.HandleFunc("/uploadtemp/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[HTTP] uploadtemp: %s", r.URL.Path)
	})
	mux.HandleFunc("/divert", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[HTTP] divert: %s to %s",
			r.FormValue("path"),
			r.FormValue("diversion"))
	})
	mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[HTTP] log: %s", r.FormValue("path"))
		w.Header().Set("Content-type", "text/event-stream")
		if r.FormValue("stream") == "stdout" {
			const text = "Hello Sun"
			line := fmt.Sprintf("data: %s\n", text)
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {}
	})
	fakeBuildTimestamp := "fake-" + time.Now().Format(time.RFC3339)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
			status := struct {
				BuildTimestamp string `json:"BuildTimestamp"`
			}{
				BuildTimestamp: fakeBuildTimestamp,
			}
			b, err := json.Marshal(&status)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
			return
		}
		http.Error(w, "handler not implemented", http.StatusNotImplemented)
	})
	srv := httptest.NewServer(mux)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	// create a new instance
	c := gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", "hello",
			"new",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

	// update the instance config to speak to the test server
	const configPath = "gokrazy/hello/config.json"
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Struct
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Update.Hostname = "localhost"
	cfg.Update.HTTPPort = u.Port()
	t.Logf("Updated cfg.Update = %+v", cfg.Update)
	b, err = cfg.FormatForFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, b, 0644); err != nil {
		t.Fatal(err)
	}

	// verify overwrite works (i.e. locates extrafiles)
	ctx := context.WithValue(context.Background(), packer.BuildTimestampOverride, fakeBuildTimestamp)
	c = gok.Context{
		Args: []string{
			"--parent_dir", "gokrazy",
			"-i", "hello",
			"update",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(ctx); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}
}
