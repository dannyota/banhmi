package http

import (
	"io"
	"net"
	"sync/atomic"

	"danny.vn/banhmi/internal/corpus/blob"
)

// responseWireConn counts plaintext HTTP response bytes before transfer decoding.
// The allowance conservatively includes headers as well as chunk framing and
// trailers. Each request uses one connection, with no reuse or HTTP/2 multiplexing.
// Atomic state crosses the transport read goroutine; receipts stay caller-owned.
type responseWireConn struct {
	net.Conn
	remaining int64
	length atomic.Int64
	exceeded atomic.Bool
}

func (c *responseWireConn) Read(p []byte) (int, error) {
	if len(p) == 0 { return 0, nil }
	if c.exceeded.Load() { return 0, blob.ErrByteLimit }
	// Read at most one byte beyond the allowance to detect a strict overrun.
	if int64(len(p)) > c.remaining && c.remaining < int64(len(p))-1 { p = p[:c.remaining+1] }
	n, err := c.Conn.Read(p)
	c.length.Add(int64(n))
	if int64(n) > c.remaining {
		c.exceeded.Store(true)
		n = int(c.remaining)
		c.remaining = 0
		return n, blob.ErrByteLimit
	}
	c.remaining -= int64(n)
	return n, err
}

type responseWireBody struct {
	io.ReadCloser
	wire *responseWireConn
	receipt *Receipt
	base int64
}

func (b *responseWireBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.receipt.WireLength = b.base + b.wire.length.Load()
	// A buffered HTTP reader may consume an entire body before surfacing the
	// connection error. Never allow that to turn a wire overrun into success.
	if b.wire.exceeded.Load() { return n, blob.ErrByteLimit }
	return n, err
}

func (b *responseWireBody) Close() error {
	err := b.ReadCloser.Close()
	b.receipt.WireLength = b.base + b.wire.length.Load()
	return err
}
