//go:build linux

// The disposable protocol entrypoint packages no converter implementation.
package main

import (
	"fmt"
	"os"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
)

func main() {
	if err := sandbox.CloseInheritedDescriptors(); err != nil {
		fmt.Fprintln(os.Stderr, "child entry: DESCRIPTORS_UNAVAILABLE")
		os.Exit(1)
	}
	if len(os.Args) != 2 || os.Args[1] != "--sandbox-child" {
		fmt.Fprintln(os.Stderr, "child entry: INVALID_ARGUMENTS")
		os.Exit(2)
	}
	if err := sandbox.ChildMain(os.Stdin, os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
