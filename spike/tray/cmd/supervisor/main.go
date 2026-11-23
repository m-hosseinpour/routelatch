// Command supervisor is the minimal parent for the lifecycle prototype.
// It starts one child, reports both PIDs, and detects child exit through
// wait(2), which is what the future `run` supervisor needs to observe a tray
// process. On SIGTERM/SIGINT it forwards termination to the child.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/spike/tray/internal/spike"
)

func main() {
	abandonAfter := flag.Duration("abandon-after", 0, "exit this long after starting the child (arm-race test)")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: supervisor [-abandon-after=D] <child-binary> [child-args...]")
		os.Exit(2)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Hand the child a pidfd on this supervisor before it exists, so the child
	// can prove parent death even if this process dies before the child arms
	// its watchdog. ExtraFiles[0] becomes fd 3 in the child.
	if pfd, err := unix.PidfdOpen(os.Getpid(), 0); err != nil {
		spike.Log("event=supervisor_pidfd_unavailable err=%q", err)
	} else {
		f := os.NewFile(uintptr(pfd), "parent-pidfd")
		defer f.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, f)
		cmd.Env = append(os.Environ(),
			spike.ParentPIDFDEnv+"=3",
			spike.ParentPIDEnv+"="+strconv.Itoa(os.Getpid()),
		)
		spike.Log("event=supervisor_pidfd_ready pidfd=%d", pfd)
	}

	if err := cmd.Start(); err != nil {
		spike.Log("event=supervisor_error err=%q", err)
		os.Exit(1)
	}
	started := time.Now()
	spike.Log("event=supervisor_ready pid=%d child_pid=%d", os.Getpid(), cmd.Process.Pid)

	if *abandonAfter > 0 {
		time.Sleep(*abandonAfter)
		spike.Log("event=supervisor_abandon exit=without_waiting")
		os.Exit(0)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case err := <-exited:
		elapsed := time.Since(started).Milliseconds()
		code := 0
		if err != nil {
			code = cmd.ProcessState.ExitCode()
		}
		spike.Log("event=child_exited detected_by=waitpid code=%d elapsed_ms=%d", code, elapsed)
		os.Exit(code)
	case s := <-sig:
		spike.Log("event=supervisor_signal signal=%s", s)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		spike.Log("event=supervisor_exit reason=signal")
		os.Exit(0)
	}
}
