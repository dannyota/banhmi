// Command healthcheck is a minimal HTTP health probe for distroless containers
// (no shell, no curl). ECS health checks invoke it as:
//
//	CMD ["/healthcheck", "http://localhost:8081/healthz"]
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: healthcheck <url>")
		os.Exit(1)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "status %d\n", resp.StatusCode)
		os.Exit(1)
	}
}
