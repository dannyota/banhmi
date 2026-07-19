package congbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Some congbao file URLs live on g7.cdnchinhphu.vn, which serves an incomplete
// TLS chain — only its leaf certificate, omitting the intermediate CA — so a
// strict client cannot build a path to a trusted root and rejects it as "signed
// by unknown authority". Browsers handle this with AIA (Authority Information
// Access) chasing: a certificate's AIA extension names a URL to its issuer's
// certificate, so the client fetches the missing intermediate and retries. Go's
// TLS stack parses that URL (x509.Certificate.IssuingCertificateURL) but does not
// fetch it, so we implement the fetch ourselves — no third-party library.
//
// This does NOT weaken verification. A fetched certificate is only added to the
// candidate intermediates; the leaf must still chain to a system trust root and
// match the hostname. A hostile AIA URL cannot forge trust — it can only help
// complete a chain that is already cryptographically valid. AIA URLs are plain
// HTTP by design (the fetched object is self-verifying), so there is no TLS
// recursion when fetching them.
const (
	aiaMaxDepth     = 5 // issuer links to chase before giving up
	aiaFetchLimit   = 1 << 20
	aiaFetchTimeout = 10 * time.Second
)

// aiaResolver verifies TLS peers against the system roots, completing an
// incomplete chain by fetching missing issuer certificates named in each cert's
// AIA extension. Fetched issuers are cached by URL so only the first connection
// to a misconfigured host pays the fetch.
type aiaResolver struct {
	roots  *x509.CertPool
	client *http.Client
	log    *slog.Logger

	mu    sync.Mutex
	cache map[string]*x509.Certificate
}

func newAIAResolver(log *slog.Logger) (*aiaResolver, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system roots: %w", err)
	}
	return &aiaResolver{
		roots:  roots,
		client: &http.Client{Timeout: aiaFetchTimeout},
		log:    log,
		cache:  make(map[string]*x509.Certificate),
	}, nil
}

// verify is the tls.Config.VerifyConnection callback. It first verifies with the
// system roots and any server-supplied intermediates; if that fails for want of
// an intermediate, it chases AIA from the leaf upward, adding fetched issuers,
// and retries. The hostname is always checked via VerifyOptions.DNSName.
func (r *aiaResolver) verify(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("tls: no peer certificates")
	}
	leaf := cs.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	opts := x509.VerifyOptions{
		DNSName:       cs.ServerName,
		Roots:         r.roots,
		Intermediates: intermediates,
	}
	if _, err := leaf.Verify(opts); err == nil {
		return nil // complete chain (e.g. the congbao gazette host) — no fetch
	}

	cur := leaf
	for depth := 0; depth < aiaMaxDepth; depth++ {
		issuer, err := r.issuerOf(cur)
		if err != nil {
			return fmt.Errorf("tls: complete chain via AIA: %w", err)
		}
		if issuer == nil {
			break // nothing left to chase
		}
		intermediates.AddCert(issuer)
		opts.Intermediates = intermediates
		if _, err := leaf.Verify(opts); err == nil {
			return nil
		}
		if bytes.Equal(issuer.RawSubject, issuer.RawIssuer) {
			break // reached a self-signed certificate
		}
		cur = issuer
	}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("tls: verify after AIA chase: %w", err)
	}
	return nil
}

// issuerOf returns the issuer certificate named in cert's AIA caIssuers URL,
// using the cache. It returns (nil, nil) when cert carries no AIA URL.
func (r *aiaResolver) issuerOf(cert *x509.Certificate) (*x509.Certificate, error) {
	if len(cert.IssuingCertificateURL) == 0 {
		return nil, nil
	}
	url := cert.IssuingCertificateURL[0]

	r.mu.Lock()
	cached, ok := r.cache[url]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	issuer, err := r.download(url)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[url] = issuer
	r.mu.Unlock()
	r.log.Debug("congbao: fetched AIA intermediate", "url", url, "subject", issuer.Subject.CommonName)
	return issuer, nil
}

// download fetches and parses one issuer certificate from an AIA URL. AIA serves
// a single DER-encoded certificate (application/pkix-cert); PEM is accepted too.
func (r *aiaResolver) download(url string) (*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), aiaFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build AIA request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, aiaFetchLimit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if block, _ := pem.Decode(body); block != nil {
		body = block.Bytes
	}
	cert, err := x509.ParseCertificate(body)
	if err != nil {
		return nil, fmt.Errorf("parse cert from %s: %w", url, err)
	}
	return cert, nil
}

// defaultHTTPClient returns the client used when the caller passes nil: a normal
// client whose TLS verification chases AIA to complete incomplete chains. If the
// system root pool cannot be loaded it logs and falls back to a plain client (CDN
// downloads may then fail, but discovery still works).
func defaultHTTPClient(logger *slog.Logger) *http.Client {
	client := &http.Client{Timeout: 60 * time.Second}
	resolver, err := newAIAResolver(logger)
	if err != nil {
		logger.Warn("congbao: AIA chain completion disabled; CDN downloads may fail", "err", err)
		return client
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, // real verification happens in aiaResolver.verify
		VerifyConnection:   resolver.verify,
	}
	client.Transport = tr
	return client
}
