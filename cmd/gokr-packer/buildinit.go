package main

import (
	"bytes"
	"fmt"
	"go/format"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
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

	var cmds []*exec.Cmd
{{- range $idx, $path := .Binaries }}
{{- if ne $path "/gokrazy/init" }}
	{
		cmd := exec.Command({{ CommandFor $.Flags $path }})
		cmd.Env = append(os.Environ(),
{{- range $idx, $env := EnvFor $.Env $path }}
			{{ printf "%q" $env }},
{{- end }}
		)
		cmds = append(cmds, cmd)
	}
{{- end }}
{{- end }}
	if err := gokrazy.Supervise(cmds); err != nil {
		log.Fatal(err)
	}
	select {}
}
`

var initTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"CommandFor": func(flags map[string]string, path string) string {
		contents := strings.TrimSpace(flags[filepath.Base(path)])
		if contents == "" {
			return fmt.Sprintf("%#v", path) // no flags
		}
		lines := strings.Split(contents, "\n")
		return fmt.Sprintf("%#v, %#v...", path, lines)
	},
	"EnvFor": func(env map[string]string, path string) []string {
		contents := strings.TrimSpace(env[filepath.Base(path)])
		if contents == "" {
			return nil // no environment variables
		}
		return strings.Split(contents, "\n")
	},
}).Parse(initTmplContents))

func flattenFiles(prefix string, root *fileInfo) []string {
	var result []string
	for _, ent := range root.dirents {
		if ent.fromHost != "" { // regular file
			result = append(result, filepath.Join(prefix, root.filename, ent.filename))
		} else { // subdir
			result = append(result, flattenFiles(filepath.Join(prefix, root.filename), ent)...)
		}
	}
	return result
}

type gokrazyInit struct {
	root             *fileInfo
	flagFileContents map[string]string
	envFileContents  map[string]string
	buildTimestamp   string
}

func (g *gokrazyInit) generate() ([]byte, error) {
	var buf bytes.Buffer

	if err := initTmpl.Execute(&buf, struct {
		Binaries       []string
		BuildTimestamp string
		Flags          map[string]string
		Env            map[string]string
	}{
		Binaries:       flattenFiles("/", g.root),
		BuildTimestamp: g.buildTimestamp,
		Flags:          g.flagFileContents,
		Env:            g.envFileContents,
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
	tmpdir, err = ioutil.TempDir("", "gokr-packer")
	if err != nil {
		return "", err
	}

	b, err := g.generate()
	if err != nil {
		return "", err
	}

	initGo := filepath.Join(tmpdir, "init.go")
	if err := ioutil.WriteFile(initGo, b, 0644); err != nil {
		return "", err
	}
	defer os.Remove(initGo)

	cmd := exec.Command("go", "build", "-o", filepath.Join(tmpdir, "init"), initGo)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %v", cmd.Args, err)
	}
	return tmpdir, nil
}
