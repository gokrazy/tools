// Binary gok is a new top-level CLI entry point for all things gokrazy:
// building and deploying new gokrazy images, managing your ~/gokrazy/
// directory, building and running Go programs from your local Go workspace,
// etc.
package main

import "github.com/gokrazy/tools/cmd/gok/cmd"

func main() {
	cmd.Execute()
}
