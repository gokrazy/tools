package gokrun_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gokrazy/tools/internal/gok"
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
	homeDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", homeDir) // what linux looks for first
	os.Setenv("HOME", homeDir)            // what darwin looks for first
	configDir := filepath.Join(homeDir, "gokrazy")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	return &gokrazyTestInstance{
		configDir: configDir,
	}
}

func writeGoWorkingDirectory(t *testing.T, wd string) {
	t.Helper()

	if err := os.MkdirAll(wd, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "go.mod"), []byte("module hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "hello.go"), []byte(`package main

import "fmt"

var world = "世界"

func main() {
	fmt.Println("Hello, " + world)
}
`), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGokRun(t *testing.T) {
	inst := writeGokrazyInstance(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/update/features", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "handler not implemented", http.StatusNotImplemented)
	})
	srv := httptest.NewServer(mux)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	inst.writeConfig(t, "http-password.txt", "irrelevant")
	inst.writeConfig(t, "http-port.txt", u.Port())

	wd := filepath.Join(t.TempDir(), "hello")
	writeGoWorkingDirectory(t, wd)
	os.Chdir(wd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Testing the root command because individual cobra commands cannot be
	// executed directly.
	root := gok.RootCmd
	root.SetContext(ctx)
	logOutputFound := make(chan bool)
	rd, wr := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(rd)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "Hello Sun" {
				logOutputFound <- true
			}
			log.Printf("line: %q", line)
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Scan(): %v", err)
		}
	}()
	root.SetOut(wr)
	root.SetErr(wr)
	args := []string{"run", "-i", "localhost"}
	root.SetArgs(args)
	t.Logf("%s", append([]string{"gok"}, args...))
	executeReturned := make(chan error)
	go func() {
		executeReturned <- root.Execute()
	}()
	for {
		select {
		case err := <-executeReturned:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Execute() = %v", err)
			}
			return
		case <-logOutputFound:
			log.Printf("/log handler requested, canceling context")
			cancel()
		}
	}

}
