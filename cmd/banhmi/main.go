// Command banhmi is the operator CLI (trigger crawls, reindex, run eval, inspect state).
package main

import (
	"flag"
	"fmt"
	"os"

	blog "danny.vn/banhmi/pkg/base/log"
)

func main() {
	flag.Parse()
	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))

	switch flag.Arg(0) {
	case "status":
		log.Info("banhmi status: M0 scaffold")
	case "eval":
		log.Info("banhmi eval: the RAG accuracy harness now lives in its own command — run `make eval` or `go run ./cmd/eval`")
	case "", "help":
		fmt.Println("usage: banhmi <status|eval>")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", flag.Arg(0))
		os.Exit(2)
	}
}
