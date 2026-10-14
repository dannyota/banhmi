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
		"::127.0.0.1", "::169.254.169.254", "::8.8.8.8",
		"64:ff9b::7f00:1", "64:ff9b:1::a00:1", "2002:7f00:1::", "2001:0:4136:e378:8000:63bf:3fff:fdd2",
		"172.16.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "192.0.0.1", "192.0.2.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "240.0.0.1", "255.255.255.255",
		"2001:db8::1", "fe80::1", "ff02::1", "fc00::1", "::", "::ffff:169.254.169.254",
	} {
		t.Run(address, func(t *testing.T) {
			c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
			if err != nil { t.Fatal(err) }
			c.lookup = func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(address)}}, nil }
			called := false
			c.connect = func(context.Context, string, string) (net.Conn, error) { called = true; return nil, errors.New("forbidden") }
			_, err = c.safeDial(context.Background(), "tcp", "source.test:443")
			var failure *Failure
			if !errors.As(err, &failure) || failure.Outcome != PolicyRejected || called { t.Fatalf("address=%s err=%v connected=%v", address, err, called) }
		})
	}
}

func TestSafeDialContextDuringDNSAndConnect(t *testing.T) {
	for _, stage := range []string{"dns", "connect"} {
		for _, deadline := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "-cancel", true: "-deadline"}[deadline], func(t *testing.T) {
				c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
				if err != nil { t.Fatal(err) }
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered := make(chan struct{})
				block := func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
				c.lookup = func(ctx context.Context, _ string) ([]net.IPAddr, error) {
					if stage == "dns" { return nil, block(ctx) }
					return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
				}
				c.connect = func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, block(ctx) }
				if !deadline { go func() { <-entered; cancel() }() }
				plan := testPlan("http://source.test/file")
				if deadline { plan.Budget.MaxElapsed = 20*time.Millisecond }
				r, fetchErr := c.Fetch(ctx, plan, []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
				err = fetchErr
				var failure *Failure
				want, reason := Infrastructure, "cancelled"
				if deadline { want, reason = PolicyRejected, "time_limit" }
				if !errors.As(err, &failure) || failure.Outcome != want || failure.Reason != reason || r.Outcome != want || r.Reason != reason { t.Fatalf("err=%v want=%s/%s", err, want, reason) }
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
		if err := checkURL(locator, []Host{{Name: "source.test", AllowHTTP: true}}, ""); err == nil { t.Fatalf("admitted %s", locator) }
		r, err := c.Fetch(context.Background(), testPlan(locator), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
		if err == nil || r.Outcome != PolicyRejected || count != 0 { t.Fatalf("destination reached network: %s receipt=%+v err=%v", locator, r, err) }
	}
}

func TestApprovedNumericSpellingsStillRejected(t *testing.T) {
	for _, host := range []string{"2130706433", "0177.0.0.1", "0x7f000001", "0x7f.0.0.1"} {
		if err := checkURL("http://"+host+"/file", []Host{{Name: host, AllowHTTP: true}}, ""); err == nil { t.Fatalf("numeric spelling approved: %s", host) }
	}
}
