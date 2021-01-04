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
	"time"
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

	cmds := []*exec.Cmd{
{{- range $idx, $path := .Binaries }}
{{- if ne $path "/gokrazy/init" }}
		exec.Command({{ CommandFor $.Flags $path }}),
{{- end }}
{{- end }}
	}
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

func genInit(root *fileInfo, flagFileContents map[string]string) ([]byte, error) {
	var buf bytes.Buffer

	if err := initTmpl.Execute(&buf, struct {
		Binaries       []string
		BuildTimestamp string
		Flags          map[string]string
	}{
		Binaries:       flattenFiles("/", root),
		BuildTimestamp: time.Now().Format(time.RFC3339),
		Flags:          flagFileContents,
	}); err != nil {
		return nil, err
	}

	return format.Source(buf.Bytes())
}

func dumpInit(path string, root *fileInfo, flagFileContents map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := genInit(root, flagFileContents)
	if err != nil {
		return err
	}

	if _, err := f.Write(b); err != nil {
		return err
	}

	return f.Close()
}

func buildInit(root *fileInfo, flagFileContents map[string]string) (tmpdir string, err error) {
	tmpdir, err = ioutil.TempDir("", "gokr-packer")
	if err != nil {
		return "", err
	}

	b, err := genInit(root, flagFileContents)
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
