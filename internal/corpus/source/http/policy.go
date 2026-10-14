// Package http implements bounded, credential-free source capture transport.
package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Host approves an exact DNS hostname and optionally cleartext HTTP. Wildcards,
// alternate ports, credentials and literal non-public addresses are not approved.
type Host struct {
	Name      string
	AllowHTTP bool
}

// MaxURLBytes bounds each admitted provenance locator independently of body size.
const MaxURLBytes = 8192

// MaxReceiptFieldBytes bounds each retained response-header field in provenance.
const MaxReceiptFieldBytes = 4096

func checkURL(locator string, hosts []Host, previousScheme string) error {
	if len(locator) > MaxURLBytes {
		return &Failure{Outcome: PolicyRejected, Reason: "url_limit"}
	}
	u, err := url.Parse(locator)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return &Failure{Outcome: PolicyRejected, Reason: "invalid_destination"}
	}
	if previousScheme == "https" && u.Scheme != "https" {
		return &Failure{Outcome: PolicyRejected, Reason: "scheme_downgrade"}
	}
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		return &Failure{Outcome: PolicyRejected, Reason: "destination_port"}
	}
	name := strings.ToLower(u.Hostname())
	if strings.HasSuffix(name, ".") || strings.Contains(name, "%") {
		return &Failure{Outcome: PolicyRejected, Reason: "destination_host"}
	}
	if ip := net.ParseIP(name); ip != nil {
		if !publicAddress(ip) {
			return &Failure{Outcome: PolicyRejected, Reason: "destination_address"}
		}
	} else if numericHost(name) {
		return &Failure{Outcome: PolicyRejected, Reason: "destination_address"}
	}
	for _, host := range hosts {
		if name == host.Name && (u.Scheme == "https" || host.AllowHTTP) {
			return nil
		}
	}
	return &Failure{Outcome: PolicyRejected, Reason: "unapproved_host"}
}

func intersection(source, plan []Host) []Host {
	var out []Host
	for _, a := range source {
		for _, b := range plan {
			if a.Name == b.Name {
				out = append(out, Host{Name: a.Name, AllowHTTP: a.AllowHTTP && b.AllowHTTP})
			}
		}
	}
	return out
}

func publicAddress(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// See the registry inventory in TestSafeDialSpecialPurposeRegistry. Block
	// enclosing protocol/transition prefixes conservatively, including their
	// globally reachable exceptions. Mapped IPv4 follows the IPv4 policy.
	for _, block := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "168.63.129.16/32",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "100:0:0:1::/64",
		"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16", "fec0::/10",
	} {
		_, network, _ := net.ParseCIDR(block)
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func (c *Client) addresses(ctx context.Context, name string) ([]net.IPAddr, error) {
	addresses, err := c.lookup(ctx, name)
	if failure := contextFailure(ctx); failure != nil {
		return nil, failure
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, classify(ctx, err)
	}
	if err != nil {
		return nil, &Failure{Outcome: Retryable, Reason: "dns_failed"}
	}
	if len(addresses) == 0 {
		return nil, &Failure{Outcome: Retryable, Reason: "dns_empty"}
	}
	for _, address := range addresses {
		if address.Zone != "" || !publicAddress(address.IP) {
			return nil, &Failure{Outcome: PolicyRejected, Reason: "destination_address"}
		}
	}
	return addresses, nil
}

func (c *Client) resolve(ctx context.Context, name string) error {
	_, err := c.addresses(ctx, name)
	return err
}

func (c *Client) safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	name, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &Failure{Outcome: PolicyRejected, Reason: "destination_address"}
	}
	addresses, err := c.addresses(ctx, name)
	if err != nil {
		return nil, err
	}
	// Validate every result, then dial a numeric address. DNS is never consulted
	// again by net.Dialer. A fresh transport prevents connection-pool rebinding.
	conn, err := c.connect(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	if failure := contextFailure(ctx); failure != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, failure
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, classify(ctx, err)
	}
	if err != nil {
		return nil, &Failure{Outcome: Retryable, Reason: "connect_failed"}
	}
	return conn, nil
}

func validateHosts(hosts []Host) error {
	seen := map[string]bool{}
	for _, host := range hosts {
		if host.Name == "" || host.Name != strings.ToLower(host.Name) || strings.ContainsAny(host.Name, "/:*@% \\") || strings.HasSuffix(host.Name, ".") || seen[host.Name] {
			return fmt.Errorf("invalid or duplicate approved host")
		}
		seen[host.Name] = true
	}
	return nil
}

// numericHost rejects noncanonical decimal, octal and hexadecimal IPv4 spellings
// even when accidentally present in an approved host list.
func numericHost(name string) bool {
	for _, part := range strings.Split(name, ".") {
		base := 10
		if strings.HasPrefix(part, "0x") {
			part, base = part[2:], 16
		}
		if _, err := strconv.ParseUint(part, base, 64); err != nil {
			return false
		}
	}
	return true
}
