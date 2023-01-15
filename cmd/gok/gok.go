// Binary gok is a new top-level CLI entry point for all things gokrazy:
// building and deploying new gokrazy images, managing your ~/gokrazy/
// directory, building and running Go programs from your local Go workspace,
// etc.
package main

import (
	"context"
	"log"

	"github.com/gokrazy/tools/gok"
)

func main() {
	if err := (gok.Context{}).Execute(context.Background()); err != nil {
		log.Fatal(err)
	}
}
