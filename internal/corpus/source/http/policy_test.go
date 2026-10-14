package http

import (
	"context"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSafeDialSpecialAddressRanges(t *testing.T) {
	for _, address := range []string{
		"0.0.0.1", "0.1.2.3", "::ffff:0.0.0.1", "::ffff:0.1.2.3",
		"::127.0.0.1", "::169.254.169.254", "::8.8.8.8",
		"64:ff9b::7f00:1", "64:ff9b:1::a00:1", "2002:7f00:1::", "2001:0:4136:e378:8000:63bf:3fff:fdd2",
		"172.16.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "192.0.0.1", "192.0.2.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "240.0.0.1", "255.255.255.255",
		"2001:db8::1", "fe80::1", "ff02::1", "fc00::1", "::", "::ffff:169.254.169.254",
	} {
		t.Run(address, func(t *testing.T) {
			c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
			}
			called := false
			c.connect = func(context.Context, string, string) (net.Conn, error) {
				called = true
				return nil, errors.New("forbidden")
			}
			_, err = c.safeDial(context.Background(), "tcp", "source.test:443")
			var failure *Failure
			if !errors.As(err, &failure) || failure.Outcome != PolicyRejected || failure.Reason != "destination_address" || called {
				t.Fatalf("address=%s err=%v connected=%v", address, err, called)
			}
		})
	}
}

func TestSafeDialContextDuringDNSAndConnect(t *testing.T) {
	for _, stage := range []string{"dns", "connect"} {
		for _, deadline := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "-cancel", true: "-deadline"}[deadline], func(t *testing.T) {
				c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered := make(chan struct{})
				block := func(ctx context.Context) error {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				}
				c.lookup = func(ctx context.Context, _ string) ([]net.IPAddr, error) {
					if stage == "dns" {
						return nil, block(ctx)
					}
					return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
				}
				c.connect = func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, block(ctx) }
				if !deadline {
					go func() {
						<-entered
						cancel()
					}()
				}
				plan := testPlan("http://source.test/file")
				if deadline {
					plan.Budget.MaxElapsed = 20 * time.Millisecond
				}
				r, fetchErr := c.Fetch(ctx, plan, []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
				err = fetchErr
				var failure *Failure
				want, reason := Infrastructure, "cancelled"
				if deadline {
					want, reason = PolicyRejected, "time_limit"
				}
				if !errors.As(err, &failure) || failure.Outcome != want || failure.Reason != reason || r.Outcome != want || r.Reason != reason {
					t.Fatalf("err=%v want=%s/%s", err, want, reason)
				}
			})
		}
	}
}

func TestDestinationSpellingsRejected(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(stdhttp.ResponseWriter, *stdhttp.Request) { count++ }))
	defer s.Close()
	c := testClient(t, s)
	for _, locator := range []string{
		"http://2130706433/file", "http://0177.0.0.1/file", "http://0x7f000001/file", "http://0x7f.0.0.1/file",
		"http://user:password@source.test/file", "http://source.test./file", "http://sourcé.test/file", "http://xn--sourc-fsa.test/file",
		"http://source.test:8080/file", "https://source.test:444/file",
	} {
		if err := checkURL(locator, []Host{{Name: "source.test", AllowHTTP: true}}, ""); err == nil {
			t.Fatalf("admitted %s", locator)
		}
		r, err := c.Fetch(context.Background(), testPlan(locator), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
		if err == nil || r.Outcome != PolicyRejected || count != 0 {
			t.Fatalf("destination reached network: %s receipt=%+v err=%v", locator, r, err)
		}
	}
}

func TestApprovedNumericSpellingsStillRejected(t *testing.T) {
	for _, host := range []string{"2130706433", "0177.0.0.1", "0x7f000001", "0x7f.0.0.1"} {
		if err := checkURL("http://"+host+"/file", []Host{{Name: host, AllowHTTP: true}}, ""); err == nil {
			t.Fatalf("numeric spelling approved: %s", host)
		}
	}
}

func TestSafeDialSpecialPurposeRegistry(t *testing.T) {
	// IANA IPv4 and IPv6 Special-Purpose Address Registries:
	// https://www.iana.org/assignments/iana-ipv4-special-registry/
	// https://www.iana.org/assignments/iana-ipv6-special-registry/
	// Every non-globally-reachable entry is rejected, including subentries.
	// Enclosing prefixes also reject global exceptions conservatively.
	// The ::ffff:0:0/96 IPv4-mapped Address entry is a representation: Go
	// normalizes it to IPv4. Public mapped addresses follow public IPv4 policy,
	// and every blocked IPv4 entry below is also tested in mapped form.
	// Additional transition, metadata, multicast and deprecated site-local
	// restrictions are listed separately from non-global registry entries.
	for _, entry := range []struct {
		prefix string
		name   string
	}{
		{"0.0.0.0/8", "This network"},
		{"0.0.0.0/32", "This host on this network"},
		{"10.0.0.0/8", "Private-Use"},
		{"100.64.0.0/10", "Shared Address Space"},
		{"127.0.0.0/8", "Loopback"},
		{"169.254.0.0/16", "Link Local"},
		{"172.16.0.0/12", "Private-Use"},
		{"192.0.0.0/24", "IETF Protocol Assignments"},
		{"192.0.0.0/29", "IPv4 Service Continuity Prefix"},
		{"192.0.0.8/32", "IPv4 dummy address"},
		{"192.0.0.170/32", "NAT64/DNS64 Discovery"},
		{"192.0.0.171/32", "NAT64/DNS64 Discovery"},
		{"192.0.2.0/24", "Documentation TEST-NET-1"},
		{"192.88.99.0/24", "Deprecated 6to4 Relay Anycast"},
		{"192.88.99.2/32", "6a44-relay anycast address"},
		{"192.168.0.0/16", "Private-Use"},
		{"198.18.0.0/15", "Benchmarking"},
		{"198.51.100.0/24", "Documentation TEST-NET-2"},
		{"203.0.113.0/24", "Documentation TEST-NET-3"},
		{"240.0.0.0/4", "Reserved"},
		{"255.255.255.255/32", "Limited Broadcast"},
		{"::/128", "Unspecified Address"},
		{"::1/128", "Loopback Address"},
		{"64:ff9b:1::/48", "IPv4-IPv6 Translat. local-use"},
		{"100::/64", "Discard-Only Address Block"},
		{"100:0:0:1::/64", "Dummy IPv6 Prefix"},
		{"2001::/23", "IETF Protocol Assignments"},
		{"2001::/32", "TEREDO, reachability N/A"},
		{"2001:2::/48", "Benchmarking"},
		{"2001:10::/28", "Deprecated ORCHID"},
		{"2001:db8::/32", "Documentation"},
		{"2002::/16", "6to4, reachability N/A"},
		{"3fff::/20", "Documentation"},
		{"5f00::/16", "Segment Routing SIDs"},
		{"fc00::/7", "Unique-Local"},
		{"fe80::/10", "Link-Local Unicast"},
		{"::/96", "Additional: deprecated IPv4-compatible"},
		{"64:ff9b::/96", "Additional: IPv4-IPv6 translation"},
		{"168.63.129.16/32", "Additional: provider metadata"},
		{"224.0.0.0/4", "Additional: IPv4 multicast"},
		{"fec0::/10", "Additional: deprecated site-local"},
		{"ff00::/8", "Additional: IPv6 multicast"},
	} {
		t.Run(entry.prefix+"-"+entry.name, func(t *testing.T) {
			ip, block, err := net.ParseCIDR(entry.prefix)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise both ends, catching accidental narrower exclusions.
			last := append(net.IP(nil), ip...)
			if v4 := ip.To4(); v4 != nil {
				ip = v4
				last = append(net.IP(nil), v4...)
			}
			for i := range last {
				last[i] |= ^block.Mask[i]
			}
			for _, address := range []net.IP{ip, last} {
				spellings := []string{address.String()}
				if address.To4() != nil {
					spellings = append(spellings, "::ffff:"+address.String())
				}
				for _, spelling := range spellings {
					c := &Client{}
					c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
						return []net.IPAddr{{IP: net.ParseIP(spelling)}}, nil
					}
					calls := 0
					c.connect = func(context.Context, string, string) (net.Conn, error) {
						calls++
						return nil, errors.New("forbidden")
					}
					_, err := c.safeDial(context.Background(), "tcp", "source.test:443")
					var failure *Failure
					if !errors.As(err, &failure) || failure.Outcome != PolicyRejected || failure.Reason != "destination_address" || calls != 0 {
						t.Fatalf("address=%s err=%v connector calls=%d", spelling, err, calls)
					}
				}
			}
		})
	}
}
