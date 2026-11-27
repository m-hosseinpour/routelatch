package tun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/internal/contract"
)

const (
	// A request is one small JSON line. Anything larger is not our protocol.
	maxControlRequest = 4096
	controlTimeout    = 35 * time.Second
	controlOpTimeout  = 30 * time.Second
)

// controlRequest is the whole input surface: one fixed command. There is no
// field for an interface, route, address or domain, so a client cannot ask the
// service to act outside the pinned configuration.
type controlRequest struct {
	Cmd string `json:"cmd"`
}

type controlResponse struct {
	OK     bool               `json:"ok"`
	Error  string             `json:"error,omitempty"`
	Reason contract.TunReason `json:"reason,omitempty"`
	Status contract.TunStatus `json:"status"`
	// SocksAddress is private to the service<->engine socket. It is the
	// loopback listener target, not a destination, and never reaches public
	// status (D21).
	SocksAddress string `json:"socks_address,omitempty"`
}

func (s *Service) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	uid, err := peerUID(uc)
	if err != nil {
		s.log.Printf("control: peer credentials unavailable")
		return
	}
	if uid != s.cfg.AllowedUID {
		// The rejection is reported by code only; the peer uid is not a
		// destination and never reaches status.
		_ = json.NewEncoder(conn).Encode(controlResponse{OK: false, Error: "peer_uid_rejected", Status: s.currentStatus(), SocksAddress: s.cfg.SocksAddress})
		return
	}

	var req controlRequest
	dec := json.NewDecoder(io.LimitReader(conn, maxControlRequest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(controlResponse{OK: false, Error: "bad_request", Status: s.currentStatus(), SocksAddress: s.cfg.SocksAddress})
		return
	}

	opCtx, cancel := context.WithTimeout(context.Background(), controlOpTimeout)
	defer cancel()

	s.mu.Lock()
	var opErr error
	switch req.Cmd {
	case "status":
	case "enable":
		opErr = s.enableLocked(opCtx)
	case "disable":
		opErr = s.disableLocked(opCtx)
	default:
		opErr = errUnknownCommand
	}
	st := s.statusLocked()
	s.mu.Unlock()

	resp := controlResponse{OK: opErr == nil, Status: st, SocksAddress: s.cfg.SocksAddress}
	if opErr != nil {
		if errors.Is(opErr, errUnknownCommand) {
			resp.Error = "unknown_command"
		} else {
			resp.Error, resp.Reason = serviceErrorCode(opErr)
		}
	}
	// An explicit disable always ends the process, even if the client
	// disconnected before the response. Otherwise a timed-out client would
	// leave a stopped service alive with a core already run, unable to enable
	// again without a process restart.
	_ = json.NewEncoder(conn).Encode(resp)
	if req.Cmd == "disable" && s.exitOnOff {
		s.requestExit(opErr)
	}
}

var errUnknownCommand = errors.New("unknown TUN control command")

func (s *Service) currentStatus() contract.TunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	var uid = -1
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credErr = err
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return -1, err
	}
	return uid, credErr
}
