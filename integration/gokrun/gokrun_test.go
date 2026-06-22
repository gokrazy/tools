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

	"github.com/gokrazy/tools/gok"
	internalgok "github.com/gokrazy/tools/internal/gok"
	"github.com/gokrazy/tools/internal/goktest"
)

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
	// create a new instance
	const (
		instanceName = "hello"
		hostname     = "localhost"
	)
	ti := goktest.WriteInstance(t, instanceName)
	parentDir := ti.ConfigDir

	c := gok.Context{
		Args: []string{
			"--parent_dir", parentDir,
			"-i", instanceName,
			"new",
		},
	}
	t.Logf("running %q", append([]string{"<gok>"}, c.Args...))
	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("%v: %v", c.Args, err)
	}

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

	{
		cfg := ti.ReadConfig(t)

		cfg.Update.HTTPPassword = "irrelevant"
		cfg.Update.HTTPPort = u.Port()
		cfg.Update.Hostname = "localhost"
		t.Logf("Updated cfg.Update = %+v", cfg.Update)

		ti.WriteConfig(t, cfg)
	}

	wd := filepath.Join(t.TempDir(), "hello")
	writeGoWorkingDirectory(t, wd)
	os.Chdir(wd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Testing the root command because individual cobra commands cannot be
	// executed directly.
	root := internalgok.RootCmd()
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
	args := []string{"run", "--parent_dir", parentDir, "-i", instanceName}
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
