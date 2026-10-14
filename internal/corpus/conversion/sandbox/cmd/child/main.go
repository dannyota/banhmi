//go:build linux

// The disposable protocol entrypoint packages no converter implementation.
package main

import (
	"os"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
)

func main() {
	if err := sandbox.CloseInheritedDescriptors(); err != nil {
		os.Exit(1)
	}
	if len(os.Args) != 2 || os.Args[1] != "--sandbox-child" {
		os.Exit(2)
	}
	if err := sandbox.ChildMain(os.Stdin, os.Stdout, nil); err != nil {
		os.Exit(1)
	}
}
