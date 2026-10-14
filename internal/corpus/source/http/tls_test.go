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
	for _, mode := range []string{"approved", "unapproved", "private", "private-dns", "redirect", "changed-peer", "residency"} {
		t.Run(mode, func(t *testing.T) {
			rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil { t.Fatal(err) }
			issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil { t.Fatal(err) }
			leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil { t.Fatal(err) }
			now := time.Now()
			rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
			_, root := testCertificate(t, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
			issuerTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "synthetic issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
			issuerDER, issuer := testCertificate(t, issuerTemplate, root, &issuerKey.PublicKey, rootKey)
			aiaURL := "http://ca.test/issuer"
			if mode == "unapproved" { aiaURL = "http://evil.test/issuer" }
			if mode == "private" { aiaURL = "http://127.0.0.1/issuer" }
			leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), DNSNames: []string{"source.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IssuingCertificateURL: []string{aiaURL}}
			leafDER, _ := testCertificate(t, leafTemplate, issuer, &leafKey.PublicKey, issuerKey)
			caCount, sourceCount, admissions := 0, 0, 0
			ca := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				caCount++
				if mode == "redirect" { stdhttp.Redirect(w, r, "http://evil.test/issuer", 302); return }
				_, _ = w.Write(issuerDER)
			}))
			defer ca.Close()
			source := httptest.NewUnstartedServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { sourceCount++; _, _ = io.WriteString(w, "verified") }))
			certificate := tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
			source.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
			if mode == "changed-peer" {
				// The replacement leaf chains to the fetched issuer but is expired.
				leafTemplate.SerialNumber = big.NewInt(4)
				leafTemplate.NotAfter = now.Add(-time.Minute)
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
			c.roots.AddCert(root)
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
			if strings.Contains(r.Reason, "http:") { t.Fatal("error leaked locator") }
		})
	}
}
