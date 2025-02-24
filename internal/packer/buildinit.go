package packer

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/gokrazy/tools/packer"
)

const initTmplContents = `
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/gokrazy/gokrazy"
)

// buildTimestamp can be overridden by specifying e.g.
// -ldflags "-X main.buildTimestamp=foo" when building.
var buildTimestamp = {{ printf "%#v" .BuildTimestamp }}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	fmt.Printf("gokrazy build timestamp %s\n", buildTimestamp)
	if err := gokrazy.Boot(buildTimestamp); err != nil {
		log.Fatal(err)
	}
	if host, err := os.Hostname(); err == nil {
		fmt.Printf("hostname %q\n", host)
	}
	if model := gokrazy.Model(); model != "" {
		fmt.Printf("gokrazy device model %s\n", model)
	}

	var services []*gokrazy.Service
{{- range $idx, $path := .Binaries }}
{{- if ne $path "/gokrazy/init" }}
	{
		cmd := exec.Command({{ CommandFor $.Flags $path }})
		cmd.Env = append(os.Environ(),
{{- range $idx, $env := EnvFor $.Env $path }}
			{{ printf "%q" $env }},
{{- end }}
		)
{{ if DontStart $.DontStart $path }}
		svc := gokrazy.NewStoppedService(cmd)
{{ else if WaitForClock $.WaitForClock $path }}
		svc := gokrazy.NewWaitForClockService(cmd)
{{ else }}
		svc := gokrazy.NewService(cmd)
{{ end }}
		services = append(services, svc)
	}
{{- end }}
{{- end }}
	if err := gokrazy.SuperviseServices(services); err != nil {
		log.Fatal(err)
	}
	select {}
}
`

var initTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"CommandFor": func(flags map[string][]string, path string) string {
		contents := flags[filepath.Base(path)]
		if len(contents) == 0 {
			return fmt.Sprintf("%#v", path) // no flags
		}
		return fmt.Sprintf("%#v, %#v...", path, contents)
	},

	"EnvFor": func(env map[string][]string, path string) []string {
		contents := env[filepath.Base(path)]
		if len(contents) == 0 {
			return nil // no environment variables
		}
		return contents
	},

	"DontStart": func(dontStart map[string]bool, path string) bool {
		return dontStart[filepath.Base(path)]
	},

	"WaitForClock": func(waitForClock map[string]bool, path string) bool {
		return waitForClock[filepath.Base(path)]
	},
}).Parse(initTmplContents))

func flattenFiles(prefix string, root *FileInfo) []string {
	var result []string
	for _, ent := range root.Dirents {
		if ent.FromHost != "" { // regular file
			result = append(result, filepath.Join(prefix, root.Filename, ent.Filename))
		} else { // subdir
			result = append(result, flattenFiles(filepath.Join(prefix, root.Filename), ent)...)
		}
	}
	return result
}

type gokrazyInit struct {
	root             *FileInfo
	flagFileContents map[string][]string
	envFileContents  map[string][]string
	dontStart        map[string]bool
	waitForClock     map[string]bool
	basenames        map[string]string
	buildTimestamp   string
}

func mapKeyBasename[M ~map[string]V, V any](basenames map[string]string, m M) M {
	r := make(M, len(m))
	for k, v := range m {
		if basename, ok := basenames[k]; ok {
			r[basename] = v
		} else {
			r[filepath.Base(k)] = v
		}
	}
	return r
}

func (g *gokrazyInit) generate() ([]byte, error) {
	var buf bytes.Buffer

	if err := initTmpl.Execute(&buf, struct {
		Binaries       []string
		BuildTimestamp string
		Flags          map[string][]string
		Env            map[string][]string
		DontStart      map[string]bool
		WaitForClock   map[string]bool
	}{
		Binaries:       flattenFiles("/", g.root),
		BuildTimestamp: g.buildTimestamp,
		Flags:          mapKeyBasename(g.basenames, g.flagFileContents),
		Env:            mapKeyBasename(g.basenames, g.envFileContents),
		DontStart:      mapKeyBasename(g.basenames, g.dontStart),
		WaitForClock:   mapKeyBasename(g.basenames, g.waitForClock),
	}); err != nil {
		return nil, err
	}

	return format.Source(buf.Bytes())
}

func (g *gokrazyInit) dump(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := g.generate()
	if err != nil {
		return err
	}

	if _, err := f.Write(b); err != nil {
		return err
	}

	return f.Close()
}

func (g *gokrazyInit) build() (tmpdir string, err error) {
	const pkg = "github.com/gokrazy/gokrazy"
	buildDir, err := packer.BuildDirOrMigrate(pkg)
	if err != nil {
		return "", fmt.Errorf("PackageDirs(%s): %v", pkg, err)
	}

	tmpdir, err = os.MkdirTemp("", "gokr-packer")
	if err != nil {
		return "", err
	}

	b, err := g.generate()
	if err != nil {
		return "", err
	}

	initGo := filepath.Join(tmpdir, "init.go")
	if err := os.WriteFile(initGo, b, 0644); err != nil {
		return "", err
	}
	defer os.Remove(initGo)

	tags := packer.DefaultTags()
	cmd := exec.Command("go",
		"build",
		"-mod=mod",
		"-o", filepath.Join(tmpdir, "init"),
		"-tags="+strings.Join(tags, ","),
		initGo)
	cmd.Dir = buildDir
	cmd.Env = packer.Env()
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return tmpdir, nil
}
