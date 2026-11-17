package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// runExit is the exit helper. It runs in its own process so that the client
// core in the parent is the only Xray instance in that process: the spike does
// not rely on two embedded cores being isolated.
func runExit() {
	dec := json.NewDecoder(os.Stdin)
	var spec exitSpec
	if err := dec.Decode(&spec); err != nil {
		fmt.Fprintf(os.Stderr, "exit: bad spec: %v\n", err)
		os.Exit(2)
	}
	b, err := buildExitConfig(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit: config: %v\n", err)
		os.Exit(2)
	}
	inst, err := startCore(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit: start: %v\n", oneLine(err))
		os.Exit(2)
	}
	fmt.Println("READY")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, os.Stdin)
		close(done) // parent closed stdin
	}()
	select {
	case <-sig:
	case <-done:
	}
	_ = inst.Close()
}

type exitProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	spec  exitSpec
}

func startExit(spec exitSpec) (*exitProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "exit")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(stdin).Encode(spec); err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	br := bufio.NewReader(stdout)
	ready := make(chan error, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ready <- err
				return
			}
			if line == "READY\n" {
				ready <- nil
				break
			}
			// Deprecation warnings and other startup logs precede READY on the
			// child's stdout. Forward them instead of treating them as READY.
			fmt.Fprint(os.Stderr, line)
		}
		io.Copy(os.Stderr, br)
	}()
	select {
	case err := <-ready:
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			return nil, fmt.Errorf("exit did not become ready: %w", err)
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("exit startup timeout")
	}
	return &exitProcess{cmd: cmd, stdin: stdin, spec: spec}, nil
}

func (e *exitProcess) stop() error {
	if e == nil {
		return nil
	}
	_ = e.stdin.Close()
	_ = e.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		_ = e.cmd.Process.Kill()
		return <-done
	}
}
