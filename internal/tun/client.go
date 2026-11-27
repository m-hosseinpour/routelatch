package tun

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"time"

	"routelatch/internal/contract"
)

const (
	// The client must outlive the service's operation budget so a slow but
	// successful enable/disable is not reported as a timeout. The service's
	// own connection deadline is controlTimeout.
	defaultClientTimeout = controlTimeout + 5*time.Second
	maxControlResponse   = 64 * 1024
)

// Client talks to the installed TUN service over its protected Unix socket. It
// is the contract.TunRuntime seam for the engine (R16-D): it never carries a
// destination, interface or route, only one of the three fixed commands.
type Client struct {
	Path    string
	Timeout time.Duration
}

var _ contract.TunRuntime = (*Client)(nil)

func (c *Client) Status(ctx context.Context) (contract.TunStatus, error) {
	return c.call(ctx, "status")
}

func (c *Client) Enable(ctx context.Context) error {
	_, err := c.call(ctx, "enable")
	return err
}

func (c *Client) Disable(ctx context.Context) error {
	_, err := c.call(ctx, "disable")
	return err
}

func (c *Client) call(ctx context.Context, cmd string) (contract.TunStatus, error) {
	if c.Path == "" {
		return contract.TunStatus{}, &contract.TunError{Kind: contract.Internal}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultClientTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "unix", c.Path)
	if err != nil {
		// A missing socket means the helper is not installed/available. Status
		// keeps the documented service_unavailable reason; a refused or broken
		// socket proves nothing about coverage, so callers keep the fail-closed
		// path for it.
		if cmd == "status" {
			return contract.TunStatus{}, &contract.TunError{Kind: contract.Internal, Reason: contract.TunReasonServiceUnavailable, Err: err}
		}
		return contract.TunStatus{}, &contract.TunError{Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable, Err: err}
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	// The whole exchange is bounded by the caller's context as well as the
	// client timeout. A context cancellation must interrupt a read that has
	// already started, not only the dial: otherwise a slow but successful
	// operation can outlive its caller and be reported late (§10.3).
	exchangeDone := make(chan struct{})
	defer close(exchangeDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-exchangeDone:
		}
	}()

	req, err := json.Marshal(controlRequest{Cmd: cmd})
	if err != nil {
		return contract.TunStatus{}, &contract.TunError{Kind: contract.Internal, Err: err}
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return contract.TunStatus{}, &contract.TunError{Kind: contract.Internal, Err: err}
	}
	var resp controlResponse
	dec := json.NewDecoder(io.LimitReader(conn, maxControlResponse))
	if err := dec.Decode(&resp); err != nil {
		return contract.TunStatus{}, &contract.TunError{Kind: contract.Internal, Err: err}
	}
	if resp.SocksAddress != "" {
		resp.Status.SocksAddress = resp.SocksAddress
	}
	if !resp.OK {
		return resp.Status, serviceFailure(resp.Error, resp.Reason)
	}
	return resp.Status, nil
}

// serviceFailure turns the private socket's error code into a contract error.
// A code or reason outside the documented set becomes internal; no runtime text
// is passed through.
func serviceFailure(code string, reason contract.TunReason) error {
	kind := contract.Internal
	switch code {
	case "unsupported":
		kind = contract.Unsupported
	case "state_locked":
		kind = contract.StateLocked
	}
	if !validTunReason(reason) {
		reason = ""
	}
	if (kind == contract.Unsupported || kind == contract.StateLocked) && reason == "" {
		kind = contract.Internal
	}
	return &contract.TunError{Kind: kind, Reason: reason}
}
