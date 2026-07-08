// Package dns provides a resilient DNS resolver that falls back to public DNS
// (Google 8.8.8.8, Cloudflare 1.1.1.1) when the system resolver fails.
//
// Call InstallFallback() once at startup to override net.DefaultResolver. All
// standard-library HTTP clients (including google.golang.org/api/idtoken and
// the Go TLS stack) then use the fallback transparently.
//
// Set BANHMI_DNS to override the fallback servers (comma-separated, e.g.
// "8.8.8.8:53,1.1.1.1:53"). Set BANHMI_DNS=system to disable fallback.
package dns

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	defaultFallbacks = []string{"8.8.8.8:53", "1.1.1.1:53"}
	installOnce      sync.Once
)

// InstallFallback replaces net.DefaultResolver with a resolver that tries the
// system resolver first, then falls back to the configured public DNS servers.
// Safe to call multiple times; only the first call takes effect.
func InstallFallback() {
	installOnce.Do(func() {
		servers := fallbackServers()
		if len(servers) == 0 {
			return
		}
		fb := &fallbackDialer{
			fallbacks: servers,
		}
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial:     fb.dial,
		}
		slog.Info("dns: fallback resolver installed", "servers", servers)
	})
}

func fallbackServers() []string {
	v := os.Getenv("BANHMI_DNS")
	if v == "system" {
		return nil
	}
	if v != "" {
		var servers []string
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if !strings.Contains(s, ":") {
				s += ":53"
			}
			servers = append(servers, s)
		}
		if len(servers) > 0 {
			return servers
		}
	}
	return defaultFallbacks
}

type fallbackDialer struct {
	fallbacks      []string
	usePublicUntil atomic.Int64 // unix nanos; if Now < this, skip system
}

const publicDNSStickDuration = 30 * time.Second

func (d *fallbackDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}

	skipSystem := time.Now().UnixNano() < d.usePublicUntil.Load()

	if !skipSystem {
		conn, err := d.dialAndProbe(ctx, dialer, network, address)
		if err == nil {
			return conn, nil
		}
		slog.Debug("dns: system resolver failed, trying fallback", "err", err)
	}

	// Try public DNS servers.
	var lastErr error
	for _, server := range d.fallbacks {
		conn, err := d.dialAndProbe(ctx, dialer, network, server)
		if err == nil {
			d.usePublicUntil.Store(time.Now().Add(publicDNSStickDuration).UnixNano())
			return conn, nil
		}
		lastErr = err
	}

	// All fallbacks failed — retry system as a last resort if we skipped it.
	if skipSystem {
		conn, err := d.dialAndProbe(ctx, dialer, network, address)
		if err == nil {
			d.usePublicUntil.Store(0)
			return conn, nil
		}
		lastErr = err
	}

	return nil, lastErr
}

// dialAndProbe dials the address and verifies the connection is usable by
// setting a short read deadline. For UDP this catches unreachable hosts
// (connect alone always succeeds for UDP).
func (d *fallbackDialer) dialAndProbe(ctx context.Context, dialer *net.Dialer, network, address string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}

	// For UDP, connect() always succeeds. Send a minimal DNS query to probe
	// reachability: a 12-byte header asking for "." (root) with RD=1.
	// If the server is unreachable, the read will fail quickly.
	if strings.HasPrefix(network, "udp") {
		probe := []byte{
			0x00, 0x01, // ID
			0x01, 0x00, // Flags: RD=1
			0x00, 0x01, // Questions: 1
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Answers/Auth/Additional: 0
			0x00,       // root label
			0x00, 0x01, // Type: A
			0x00, 0x01, // Class: IN
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write(probe); err != nil {
			_ = conn.Close()
			return nil, err
		}
		buf := make([]byte, 64)
		if _, err := conn.Read(buf); err != nil {
			_ = conn.Close()
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
	}

	return conn, nil
}
