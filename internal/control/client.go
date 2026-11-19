package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"routelatch/internal/contract"
)

// ErrNotRunning means the management socket is absent or refused. The CLI maps
// it to exit 3; it is not a routing failure.
var ErrNotRunning = errors.New("the RouteLatch management socket is not available")

// Client is the CLI/menu side of the management protocol. It never reads or
// writes state files; it only talks to the running service.
type Client struct {
	Path    string
	Timeout time.Duration
}

// Call sends one request and returns the decoded envelope. A failure to reach
// the socket (absent or refusing dials) returns ErrNotRunning; an error envelope
// is returned with a nil error so the caller can print it and use
// ExitCodeForEnvelope.
func (c *Client) Call(ctx context.Context, req Request) (contract.Envelope, error) {
	if c.Path == "" {
		return contract.Envelope{}, ErrNotRunning
	}
	req.SchemaVersion = protocolVersion
	line, err := json.Marshal(req)
	if err != nil {
		return contract.Envelope{}, err
	}
	if len(line) > contract.MaxManagementRequest {
		return contract.Envelope{}, &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "control.client",
			Message: "request exceeds the 64 KiB management limit",
		}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// pool test and diagnose can legitimately exceed the default request
	// deadline; the server enforces the per-operation bound as well.
	switch req.Op {
	case OpPoolTest:
		timeout = 10 * time.Minute
	case OpDiagnose, OpProposalsRecheck:
		timeout = 90 * time.Second
	case OpPoolRefresh:
		timeout = 95 * time.Second
	case OpImport, OpPoolAdd:
		timeout = 65 * time.Second
	case OpDesktopSet, OpDesktopRestore, OpBackup, OpRestore:
		timeout = 30 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) ||
			errors.Is(err, syscall.ENOENT) {
			return contract.Envelope{}, ErrNotRunning
		}
		return contract.Envelope{}, ErrNotRunning
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	// The server checks the peer before reading the request: a rejected peer is
	// answered and disconnected immediately. Our write can therefore fail with
	// EPIPE/ECONNRESET after the dial succeeded, while the reject envelope is
	// already buffered. ErrNotRunning means the socket is absent or refusing
	// dials, so only a dial error may become it; read the response first.
	_, writeErr := conn.Write(append(line, '\n'))
	raw, err := readLineLimit(bufio.NewReader(conn), contract.MaxManagementResponse)
	if err != nil {
		return contract.Envelope{}, timeoutOr(err, err)
	}
	var env contract.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if writeErr != nil {
			// No usable response arrived and the request could not be sent;
			// surface the real transport error, not a misleading JSON/timeout.
			return contract.Envelope{}, writeErr
		}
		return contract.Envelope{}, &contract.Failure{
			Kind: contract.Internal, Path: contract.PathLocal, Op: "control.client",
			Message: "management response is not valid JSON",
		}
	}
	if env.SchemaVersion != protocolVersion {
		return contract.Envelope{}, &contract.Failure{
			Kind: contract.Unsupported, Path: contract.PathLocal, Op: "control.client",
			Message: "management response has an unsupported schema version",
		}
	}
	return env, nil
}

// timeoutOr turns our own wait deadline into the documented timeout failure so
// the CLI exits 8 instead of reporting a raw socket error as exit 1.
func timeoutOr(err error, fallback error) error {
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return &contract.Failure{Kind: contract.Timeout, Path: contract.PathLocal, Op: "control.client", Message: "the management request timed out"}
	}
	return fallback
}

// readLineLimit reads one newline-terminated line and rejects a line longer
// than limit bytes. A line without a newline at EOF is accepted only if it fits.
func readLineLimit(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(buf)+len(part) > limit+1 {
			return nil, &contract.Failure{
				Kind: contract.Internal, Path: contract.PathLocal, Op: "control.read",
				Message: "message exceeds the management size limit",
			}
		}
		buf = append(buf, part...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return nil, err
	}
	if len(buf) > 0 && buf[len(buf)-1] == '\n' {
		buf = buf[:len(buf)-1]
	}
	if len(buf) > limit {
		return nil, &contract.Failure{
			Kind: contract.Internal, Path: contract.PathLocal, Op: "control.read",
			Message: "message exceeds the management size limit",
		}
	}
	return buf, nil
}

// peerUID reads SO_PEERCRED on a connected Unix socket. It is the default peer
// check; tests inject a different function to simulate a foreign uid.
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return cred.Uid, nil
}
