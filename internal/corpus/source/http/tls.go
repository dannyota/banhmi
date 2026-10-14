package http

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"time"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/source/api"
)

// MaxIntermediateDepth bounds certificate chain completion without source retries.
const MaxIntermediateDepth = 3

func (c *Client) handshake(ctx context.Context, network, address string, config *tls.Config) (*tls.Conn, error) {
	conn, err := c.dial(ctx, network, address)
	if err != nil { return nil, err }
	tlsConn := tls.Client(conn, config)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (c *Client) dialTLS(ctx context.Context, network, address string, plan api.RequestPlan, authorize Authorize, receipt *Receipt, allowAIA bool) (net.Conn, error) {
	name, _, err := net.SplitHostPort(address)
	if err != nil { return nil, &Failure{Outcome: PolicyRejected, Reason: "destination_address"} }
	config := c.verifyingTLSConfig(name, nil)
	conn, err := c.handshake(ctx, network, address, config)
	if err == nil { return conn, nil }
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	if !allowAIA || !errors.As(err, &verification) || !errors.As(verification.Err, &unknown) || len(verification.UnverifiedCertificates) == 0 {
		return nil, err
	}
	leaf := verification.UnverifiedCertificates[0]
	if leaf.VerifyHostname(name) != nil { return nil, &Failure{Outcome: Permanent, Reason: "tls_hostname"} }
	intermediates := x509.NewCertPool()
	fingerprints := map[string]bool{}
	for _, cert := range verification.UnverifiedCertificates {
		fingerprints[contentDigest(cert.Raw)] = true
	}
	for _, cert := range verification.UnverifiedCertificates[1:] { intermediates.AddCert(cert) }
	opts := c.verificationOptions(name, intermediates)
	current := leaf
	for _, cert := range verification.UnverifiedCertificates[1:] {
		if current.CheckSignatureFrom(cert) == nil { current = cert }
	}
	visited := map[string]bool{}
	var fetched []*x509.Certificate
	for depth := 0; depth < MaxIntermediateDepth; depth++ {
		if len(current.IssuingCertificateURL) == 0 { return nil, &Failure{Outcome: Permanent, Reason: "tls_missing_issuer"} }
		locator := current.IssuingCertificateURL[0]
		if visited[locator] { return nil, &Failure{Outcome: Permanent, Reason: "tls_chain_cycle"} }
		if pageCount(*receipt) >= plan.Budget.MaxPages { return nil, &Failure{Outcome: PolicyRejected, Reason: "certificate_page_limit"} }
		if err := checkURL(locator, c.config.CAHosts, ""); err != nil { return nil, err }
		issuer, err := c.fetchIssuer(ctx, locator, plan, authorize, receipt, visited)
		if err != nil { return nil, err }
		fingerprint := contentDigest(issuer.Raw)
		if fingerprints[fingerprint] { return nil, &Failure{Outcome: Permanent, Reason: "tls_chain_cycle"} }
		fingerprints[fingerprint] = true
		if !issuer.IsCA || current.CheckSignatureFrom(issuer) != nil { return nil, &Failure{Outcome: Permanent, Reason: "tls_invalid_issuer"} }
		intermediates.AddCert(issuer)
		fetched = append(fetched, issuer)
		if _, err := leaf.Verify(opts); err == nil {
			// Reverify the actual second peer against system roots and the approved
			// request hostname. Downloaded certificates remain intermediates only.
			return c.handshake(ctx, network, address, c.verifyingTLSConfig(name, fetched))
		} else if !errors.As(err, &unknown) {
			return nil, &Failure{Outcome: Permanent, Reason: "tls_verification_failed"}
		}
		current = issuer
	}
	return nil, &Failure{Outcome: Permanent, Reason: "tls_chain_depth"}
}

func (c *Client) verificationOptions(name string, intermediates *x509.CertPool) x509.VerifyOptions {
	return x509.VerifyOptions{Roots: c.roots, Intermediates: intermediates, DNSName: name,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: c.now()}
}

// verifyingTLSConfig is the only client TLS config constructor. The skip flag
// and full verifier are inseparable; no session cache permits resumption.
func (c *Client) verifyingTLSConfig(name string, fetched []*x509.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: c.roots, ServerName: name,
		InsecureSkipVerify: true, // VerifyConnection performs full chain and hostname verification.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 { return &Failure{Outcome: Permanent, Reason: "tls_missing_peer"} }
			pool := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] { pool.AddCert(cert) }
			for _, cert := range fetched { pool.AddCert(cert) }
			_, err := state.PeerCertificates[0].Verify(c.verificationOptions(name, pool))
			if err != nil { return &tls.CertificateVerificationError{UnverifiedCertificates: state.PeerCertificates, Err: err} }
			return nil
		},
	}
}

func (c *Client) fetchIssuer(ctx context.Context, locator string, plan api.RequestPlan, authorize Authorize, parent *Receipt, visited map[string]bool) (*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, min(plan.Budget.MaxElapsed, 10*time.Second))
	defer cancel()
	plan.URL, plan.Method, plan.Body, plan.Headers = locator, "GET", nil, nil
	plan.Budget.MaxPages -= pageCount(*parent)
	plan.Budget.MaxInputBytes = min(plan.Budget.MaxInputBytes, int64(1<<20))
	r := Receipt{RequestKey: plan.RequestKey, Started: time.Now().UTC(), FinalURL: locator}
	resp, err := c.request(ctx, plan, c.config.CAHosts, authorize, stdhttp.Header{}, &r, false, visited)
	var content []byte
	if err == nil {
		r.Status, r.FinalURL = resp.StatusCode, resp.Request.URL.String()
		r.MediaType, r.RetryAfter = resp.Header.Get("Content-Type"), resp.Header.Get("Retry-After")
		if resp.StatusCode != stdhttp.StatusOK {
			outcome := Permanent
			if resp.StatusCode == 429 || resp.StatusCode == 403 || resp.StatusCode >= 500 { outcome = Retryable }
			err = &Failure{Outcome: outcome, Reason: "certificate_status"}
		} else if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
			err = &Failure{Outcome: PolicyRejected, Reason: "certificate_encoding"}
		} else {
			content, err = io.ReadAll(&boundedReader{reader: resp.Body, remaining: plan.Budget.MaxInputBytes})
			r.ByteLength = int64(len(content))
			r.SHA256 = contentDigest(content)
		}
		_ = resp.Body.Close()
	}
	if err != nil {
		if errors.Is(err, blob.ErrByteLimit) { err = &Failure{Outcome: PolicyRejected, Reason: "byte_limit"} } else { err = classify(ctx, err) }
	}
	r, err = finish(r, err)
	parent.Intermediates = append(parent.Intermediates, r)
	if err != nil && r.RetryAfter != "" { parent.RetryAfter = r.RetryAfter }
	if err != nil { return nil, err }
	if block, _ := pem.Decode(content); block != nil { content = block.Bytes }
	cert, err := x509.ParseCertificate(content)
	if err != nil { return nil, &Failure{Outcome: Permanent, Reason: "certificate_format"} }
	return cert, nil
}

func contentDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
