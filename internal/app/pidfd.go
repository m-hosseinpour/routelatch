package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The launcher and teardown paths signal only through pidfds. These variables
// are seams for deterministic failure tests; production uses the kernel
// syscalls directly.
var (
	pidfdOpen       = unix.PidfdOpen
	pidfdSendSignal = unix.PidfdSendSignal
	procRoot        = "/proc"
)

var (
	// ErrGone means the process is already dead (ESRCH).
	ErrGone = errors.New("process is already gone")
	// ErrNotPermitted means the kernel refused a pidfd operation (EPERM).
	ErrNotPermitted = errors.New("pidfd operation is not permitted")
	// ErrTeardownIncomplete means a reported process or group member survived
	// the bounded teardown. It must never be reported as a readiness timeout.
	ErrTeardownIncomplete = errors.New("teardown left a live or unverified process")
)

// ProbePIDFD checks pidfd support on this process before any child is spawned
// (docs/contracts.md §7.5). ENOSYS and EPERM are the documented unsupported
// outcomes.
func ProbePIDFD() error {
	fd, err := pidfdOpen(os.Getpid(), 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			return err
		}
		return err
	}
	return unix.Close(fd)
}

// Identity is the (pid, starttime) pair used to detect PID reuse.
type Identity struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"starttime"`
}

// ProcHandle pins one process with an open pidfd and its verified starttime.
type ProcHandle struct {
	Identity
	fd int
}

// OpenProc opens a pidfd and then reads the starttime of that same pinned
// process. A failure after pidfd_open closes the descriptor.
func OpenProc(pid int) (*ProcHandle, error) {
	fd, err := pidfdOpen(pid, 0)
	if err != nil {
		return nil, classifyPidfd(err)
	}
	start, _, _, err := readProcStat(pid)
	if err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrGone
		}
		return nil, err
	}
	return &ProcHandle{Identity: Identity{PID: pid, StartTime: start}, fd: fd}, nil
}

func classifyPidfd(err error) error {
	switch {
	case errors.Is(err, unix.ESRCH):
		return ErrGone
	case errors.Is(err, unix.ENOSYS):
		return fmt.Errorf("%w: %v", ErrNotPermitted, err)
	case errors.Is(err, unix.EPERM):
		return fmt.Errorf("%w: %v", ErrNotPermitted, err)
	default:
		return err
	}
}

// Signal sends one signal through the pidfd. ESRCH is not an error: the target
// is already dead, which is the desired end state. EPERM and every other error
// are reported to the caller as an unsafe teardown.
func (h *ProcHandle) Signal(sig syscall.Signal) error {
	if h == nil || h.fd < 0 {
		return ErrGone
	}
	err := pidfdSendSignal(h.fd, unix.Signal(sig), nil, 0)
	if err == nil || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if errors.Is(err, unix.EPERM) {
		return fmt.Errorf("%w: %v", ErrNotPermitted, err)
	}
	return err
}

// Fd exposes the pidfd for polling. It must not be closed by the caller.
func (h *ProcHandle) Fd() int {
	if h == nil {
		return -1
	}
	return h.fd
}

// Close releases the pidfd. Closing it does not signal the process.
func (h *ProcHandle) Close() {
	if h == nil || h.fd < 0 {
		return
	}
	_ = unix.Close(h.fd)
	h.fd = -1
}

// Alive reports whether the process is not a zombie and still present. A
// missing /proc entry is dead.
func (h *ProcHandle) Alive() bool {
	if h == nil {
		return false
	}
	_, state, _, err := readProcStat(h.PID)
	if err != nil {
		return false
	}
	return state != 'Z'
}

// WaitExit waits until the pidfd is readable (the process exited) or the
// timeout expires. It returns true on confirmed exit/zombie.
func (h *ProcHandle) WaitExit(timeout time.Duration) bool {
	if h == nil || h.fd < 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return !h.Alive()
		}
		ms := int(remaining.Milliseconds())
		if ms < 1 {
			ms = 1
		}
		fds := []unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return !h.Alive()
		}
		if n > 0 || !h.Alive() {
			return true
		}
	}
}

// Terminate sends SIGTERM, waits termWait, then SIGKILL, waits killWait. It
// reports ErrTeardownIncomplete if the process is still alive and not a
// zombie.
func (h *ProcHandle) Terminate(termWait, killWait time.Duration) error {
	if h == nil || !h.Alive() {
		return nil
	}
	if err := h.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	if h.WaitExit(termWait) {
		return nil
	}
	if err := h.Signal(syscall.SIGKILL); err != nil {
		return err
	}
	if h.WaitExit(killWait) {
		return nil
	}
	return ErrTeardownIncomplete
}

// readProcStat returns state, process group and starttime for pid. comm may
// contain spaces and parentheses, so parsing starts after the final ')'.
func readProcStat(pid int) (start uint64, state byte, pgrp int, err error) {
	raw, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procRoot, pid))
	if err != nil {
		return 0, 0, 0, err
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 || end+2 >= len(text) {
		return 0, 0, 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(text[end+2:])
	// fields[0]=state, [1]=ppid, [2]=pgrp, [19]=starttime.
	if len(fields) < 20 || len(fields[0]) == 0 {
		return 0, 0, 0, fmt.Errorf("malformed /proc/%d/stat fields", pid)
	}
	state = fields[0][0]
	group, convErr := strconv.Atoi(fields[2])
	if convErr != nil {
		return 0, 0, 0, fmt.Errorf("malformed /proc/%d/stat pgrp: %w", pid, convErr)
	}
	startValue, convErr := strconv.ParseUint(fields[19], 10, 64)
	if convErr != nil {
		return 0, 0, 0, fmt.Errorf("malformed /proc/%d/stat starttime: %w", pid, convErr)
	}
	return startValue, state, group, nil
}

// ProcessMember is one live, non-zombie member of a process group.
type ProcessMember struct {
	Identity
	State byte
	Pgrp  int
}

// GroupMembers scans /proc for live (non-zombie) members of pgid.
func GroupMembers(pgid int) ([]ProcessMember, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var out []ProcessMember
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid == os.Getpid() {
			continue
		}
		start, state, group, statErr := readProcStat(pid)
		if statErr != nil || state == 'Z' || group != pgid {
			continue
		}
		out = append(out, ProcessMember{Identity: Identity{PID: pid, StartTime: start}, State: state, Pgrp: group})
	}
	return out, nil
}

// Teardown is the documented launcher failure path: signal the supervisor
// through its pidfd, wait for waitpid confirmation, terminate every reported
// child through its pidfd, then scan the old process group while the
// supervisor pidfd is still open. Only a clean result may become exit 8.
func Teardown(supervisor *ProcHandle, children []*ProcHandle, termWait, killWait time.Duration) error {
	if supervisor == nil {
		return ErrTeardownIncomplete
	}
	if err := supervisor.Terminate(termWait, killWait); err != nil {
		return err
	}
	incomplete := false
	for _, child := range children {
		if child == nil || child.PID == supervisor.PID {
			continue
		}
		if err := child.Terminate(termWait, killWait); err != nil {
			incomplete = true
		}
	}
	// Keep the supervisor pidfd open during the group scan so its PID/PGID
	// cannot be reused while we decide whether the group is empty.
	members, err := GroupMembers(supervisor.PID)
	if err != nil {
		incomplete = true
	}
	if len(members) > 0 {
		incomplete = true
	}
	if incomplete {
		return ErrTeardownIncomplete
	}
	return nil
}
