package http

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCertificate(t *testing.T, template, parent *x509.Certificate, public any, signer any) ([]byte, *x509.Certificate) {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, parent, public, signer)
	if err != nil { t.Fatal(err) }
	cert, err := x509.ParseCertificate(der)
	if err != nil { t.Fatal(err) }
	return der, cert
}

func TestIncompleteTLSChainRequiresRestrictedIssuerFetch(t *testing.T) {
	for _, mode := range []string{"approved", "unapproved", "private", "private-dns", "redirect", "changed-peer", "changed-peer-expired", "wrong-hostname", "tls11", "oversized", "non-certificate", "self-signed", "url-cycle", "fingerprint-cycle", "root-only", "chunked-aia", "residency"} {
		t.Run(mode, func(t *testing.T) {
			rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil { t.Fatal(err) }
			issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil { t.Fatal(err) }
			leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil { t.Fatal(err) }
			now := time.Now()
			rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
			if mode == "changed-peer" {
				rootTemplate.PermittedDNSDomainsCritical = true
				rootTemplate.PermittedDNSDomains = []string{"source.test"}
			}
			_, root := testCertificate(t, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
			issuerTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "synthetic issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
			if mode == "url-cycle" || mode == "self-signed" { issuerTemplate.IssuingCertificateURL = []string{"http://ca.test/issuer"} }
			if mode == "fingerprint-cycle" { issuerTemplate.IssuingCertificateURL = []string{"http://ca.test/alternate"} }
			if mode == "root-only" { issuerTemplate.IssuingCertificateURL = []string{"http://ca.test/root"} }
			issuerDER, issuer := testCertificate(t, issuerTemplate, root, &issuerKey.PublicKey, rootKey)
			if mode == "self-signed" { issuerDER, issuer = testCertificate(t, issuerTemplate, issuerTemplate, &issuerKey.PublicKey, issuerKey) }
			aiaURL := "http://ca.test/issuer"
			if mode == "unapproved" { aiaURL = "http://evil.test/issuer" }
			if mode == "private" { aiaURL = "http://127.0.0.1/issuer" }
			leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), DNSNames: []string{"source.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IssuingCertificateURL: []string{aiaURL}}
			if mode == "wrong-hostname" { leafTemplate.DNSNames = []string{"wrong.test"} }
			leafDER, leaf := testCertificate(t, leafTemplate, issuer, &leafKey.PublicKey, issuerKey)
			if mode == "root-only" {
				// Prove this fixture would pass if its downloaded issuer became a root.
				unsafeRoots := x509.NewCertPool(); unsafeRoots.AddCert(issuer)
				if _, err := leaf.Verify(x509.VerifyOptions{Roots: unsafeRoots, DNSName: "source.test"}); err != nil { t.Fatalf("root-only control cannot expose trust promotion: %v", err) }
			}
			caCount, sourceCount, admissions := 0, 0, 0
			ca := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				caCount++
				if mode == "redirect" { stdhttp.Redirect(w, r, "http://evil.test/issuer", 302); return }
				if mode == "oversized" { _, _ = io.WriteString(w, strings.Repeat("x", 5000)); return }
				if mode == "non-certificate" { _, _ = io.WriteString(w, "not a certificate"); return }
				if mode == "chunked-aia" {
					conn, out, err := w.(stdhttp.Hijacker).Hijack()
					if err != nil { t.Error(err); return }
					defer func() { _ = conn.Close() }()
					_, _ = out.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"+strings.Repeat("1\r\nx\r\n", 1000)+"0\r\n\r\n")
					_ = out.Flush()
					return
				}
				if r.URL.Path == "/root" { _, _ = w.Write(root.Raw); return }
				_, _ = w.Write(issuerDER)
			}))
			defer ca.Close()
			source := httptest.NewUnstartedServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { sourceCount++; _, _ = io.WriteString(w, "verified") }))
			certificate := tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
			source.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
			if mode == "tls11" { source.TLS.MinVersion, source.TLS.MaxVersion = tls.VersionTLS11, tls.VersionTLS11 }
			if mode == "changed-peer" || mode == "changed-peer-expired" {
				// Keep the approved SAN but add a SAN forbidden by the trusted root.
				// The separate expired-peer case retains the original expiry assertion.
				leafTemplate.SerialNumber = big.NewInt(4)
				if mode == "changed-peer-expired" { leafTemplate.NotAfter = now.Add(-time.Minute) } else { leafTemplate.DNSNames = []string{"source.test", "forbidden.test"} }
				badDER, _ := testCertificate(t, leafTemplate, issuer, &leafKey.PublicKey, issuerKey)
				handshakes := 0
				source.TLS.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
					handshakes++
					if handshakes > 1 { return &tls.Certificate{Certificate: [][]byte{badDER}, PrivateKey: leafKey}, nil }
					return &certificate, nil
				}
				source.TLS.Certificates = nil
			}
			source.StartTLS()
			defer source.Close()
			c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test"}}}, CAHosts: []Host{{Name: "ca.test", AllowHTTP: true}, {Name: "127.0.0.1", AllowHTTP: true}}})
			if err != nil { t.Fatal(err) }
			c.roots = x509.NewCertPool()
			if mode != "root-only" && mode != "url-cycle" && mode != "fingerprint-cycle" { c.roots.AddCert(root) }
			c.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
				address := "8.8.8.8"
				if host == "ca.test" { address = "8.8.4.4" }
				if host == "ca.test" && mode == "private-dns" { address = "10.0.0.1" }
				return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
			}
			// Preserve the production address checks, replacing only the numeric
			// socket connection with loopback destinations for synthetic servers.
			c.connect = func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(address)
				destination := source.Listener.Addr().String()
				if host == "8.8.4.4" { destination = ca.Listener.Addr().String() }
				return (&net.Dialer{}).DialContext(ctx, network, destination)
			}
			r, err := c.Fetch(context.Background(), testPlan("https://source.test/file"), []Host{{Name: "source.test"}}, func(context.Context, *stdhttp.Request) error {
				admissions++
				if mode == "residency" && admissions > 1 { return &Failure{Outcome: PolicyRejected, Reason: "residency_rejected"} }
				return nil
			}, io.Discard)
			if mode == "approved" {
				if err != nil || sourceCount != 1 || caCount != 1 || admissions != 2 || len(r.Intermediates) != 1 || r.Intermediates[0].SHA256 == "" || r.SHA256 != contentDigest([]byte("verified")) { t.Fatalf("receipt=%+v err=%v source=%d ca=%d admissions=%d", r, err, sourceCount, caCount, admissions) }
			} else if err == nil || sourceCount != 0 { t.Fatalf("unsafe TLS succeeded: receipt=%+v err=%v", r, err) }
			if (mode == "unapproved" || mode == "private" || mode == "private-dns" || mode == "residency") && caCount != 0 { t.Fatal("unapproved certificate fetch reached network") }
			if (mode == "wrong-hostname" || mode == "tls11") && caCount != 0 { t.Fatal("invalid peer triggered AIA") }
			if (mode == "url-cycle" || mode == "self-signed") && (caCount != 1 || r.Reason != "tls_chain_cycle") { t.Fatalf("cycle retried: %+v ca=%d", r, caCount) }
			if mode == "fingerprint-cycle" && (caCount != 2 || r.Reason != "tls_chain_cycle") { t.Fatalf("fingerprint cycle missed: %+v ca=%d", r, caCount) }
			if mode == "root-only" && r.Outcome != Permanent { t.Fatalf("untrusted chain outcome: %+v", r) }
			if (mode == "oversized" || mode == "chunked-aia") && (r.Reason != "byte_limit" || len(r.Intermediates) != 1 || r.Intermediates[0].Outcome != PolicyRejected) { t.Fatalf("AIA limit missed: %+v", r) }
			if mode == "redirect" && (len(r.Intermediates) != 1 || r.Intermediates[0].Status != 302 || r.Intermediates[0].FinalURL != aiaURL) { t.Fatalf("AIA response lost: %+v", r) }
			if strings.Contains(r.Reason, "http:") { t.Fatal("error leaked locator") }
		})
	}
}

func TestTLSConfigBindsApprovedNameClockAndSystemRoots(t *testing.T) {
	s := httptest.NewTLSServer(stdhttp.HandlerFunc(func(stdhttp.ResponseWriter, *stdhttp.Request) {}))
	defer s.Close()
	c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test"}}}})
	if err != nil { t.Fatal(err) }
	c.roots = x509.NewCertPool()
	c.roots.AddCert(s.Certificate())
	name := s.Certificate().DNSNames[0]
	config := c.verifyingTLSConfig(name, nil)
	if !config.InsecureSkipVerify || config.VerifyConnection == nil || config.MinVersion != tls.VersionTLS12 || config.ClientSessionCache != nil || config.RootCAs != c.roots { t.Fatal("TLS configuration contract changed") }
	state := tls.ConnectionState{ServerName: "unapproved.test", PeerCertificates: []*x509.Certificate{s.Certificate()}}
	if err := config.VerifyConnection(state); err != nil { t.Fatalf("verifier used peer ServerName instead of approved request name: %v", err) }
	c.now = func() time.Time { return s.Certificate().NotAfter.Add(time.Hour) }
	if err := config.VerifyConnection(state); err == nil { t.Fatal("verifier ignored the client's clock") }
}
