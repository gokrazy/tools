package main

import (
	"bytes"
	"go/format"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
	"time"
)

const initTmplContents = `
package main

import (
	"fmt"
	"log"
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

	cmds := []*exec.Cmd{
{{- range $idx, $path := .Binaries }}
{{- if ne $path "/gokrazy/init" }}
		exec.Command({{ printf "%#v" $path }}),
{{- end }}
{{- end }}
	}
	if err := gokrazy.Supervise(cmds); err != nil {
		log.Fatal(err)
	}
	select {}
}
`

var initTmpl = template.Must(template.New("").Parse(initTmplContents))

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

func dumpInit(path string, root *fileInfo) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	if err := initTmpl.Execute(&buf, struct {
		Binaries       []string
		BuildTimestamp string
	}{
		Binaries:       flattenFiles("/", root),
		BuildTimestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		return err
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return err
	}

	if _, err := f.Write(formatted); err != nil {
		return err
	}

	return f.Close()
}

func buildInit(root *fileInfo) (tmpdir string, err error) {
	tmpdir, err = ioutil.TempDir("", "gokr-packer")
	if err != nil {
		return "", err
	}

	code, err := os.Create(filepath.Join(tmpdir, "init.go"))
	if err != nil {
		return "", err
	}
	defer os.Remove(code.Name())

	if err := initTmpl.Execute(code, struct {
		Binaries       []string
		BuildTimestamp string
	}{
		Binaries:       flattenFiles("/", root),
		BuildTimestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		return "", err
	}

	if err := code.Close(); err != nil {
		return "", err
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(tmpdir, "init"), code.Name())
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return tmpdir, nil
}
