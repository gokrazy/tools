// Package gok allows running the gok CLI from Go code programmatically, to
// build abstractions on top of gokrazy easily.
package gok

import (
	"context"
	"io"

	"github.com/gokrazy/tools/internal/gok"
)

type Context struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Args   []string
}

func (c Context) Execute(ctx context.Context) error {
	root := gok.RootCmd()
	if r := c.Stdin; r != nil {
		root.SetIn(r)
	}
	if w := c.Stdout; w != nil {
		root.SetOut(w)
	}
	if w := c.Stderr; w != nil {
		root.SetErr(w)
	}
	if args := c.Args; args != nil {
		root.SetArgs(args)
	}
	root.SetContext(ctx)
	return root.Execute()
}
