package ipc

import (
	"context"
	"fmt"
	"time"

	"github.com/Microsoft/go-winio"
)

// Client is a thin convenience wrapper for tests and internal callers
// that need to talk to the daemon the same way the C++ shim does. It is
// NOT used by the TSF shim (which has its own C++ pipe client).
type Client struct {
	pipeName string
	timeout  time.Duration
}

// NewClient returns a client for the given pipe path with a per-call
// connect+read/write timeout. The pipe is reconnected on every Call —
// fine for tests and infrequent control-plane ops, too slow for the
// per-keystroke hot path.
func NewClient(pipeName string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Client{pipeName: pipeName, timeout: timeout}
}

// Call sends one request and returns the response. It opens a fresh
// connection each time — do not use this on the IME hot path.
func (c *Client) Call(ctx context.Context, req Request) (Response, error) {
	timeout := c.timeout
	conn, err := winio.DialPipe(c.pipeName, &timeout)
	if err != nil {
		return Response{}, fmt.Errorf("ipc: dial %s: %w", c.pipeName, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else if c.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.timeout))
	}

	if err := WriteFrame(conn, &req); err != nil {
		return Response{}, fmt.Errorf("ipc: write: %w", err)
	}
	var resp Response
	if err := ReadFrame(conn, &resp); err != nil {
		return Response{}, fmt.Errorf("ipc: read: %w", err)
	}
	return resp, nil
}

// Ping is a convenience for connectivity checks. Returns nil if the
// daemon answered the ping with Accepted=true.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.Call(ctx, Request{Op: OpPing})
	if err != nil {
		return err
	}
	if !resp.Accepted {
		return fmt.Errorf("ipc: ping not accepted: %s", resp.Error)
	}
	return nil
}

// (imports kept minimal; add net when we add a connection pool)
