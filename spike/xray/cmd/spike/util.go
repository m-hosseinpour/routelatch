package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// reporter serializes the evidence output. Every line is labelled so that a
// captured run can be read without guessing which scenario produced it.
type reporter struct {
	mu sync.Mutex
	w  io.Writer
}

func newReporter(w io.Writer) *reporter { return &reporter{w: w} }

func (r *reporter) line(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, format+"\n", args...)
}

func (r *reporter) section(name string) {
	r.line("")
	r.line("=== %s", name)
}

func (r *reporter) kv(k string, v any) { r.line("    %-22s %v", k+":", v) }

func dur(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.1fµs", float64(d)/float64(time.Microsecond))
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.3fs", d.Seconds())
	}
}

func delta(from, to time.Time) string { return dur(to.Sub(from)) }

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// oneLine keeps multi-line Xray error chains readable in the report.
func oneLine(err error) string {
	if err == nil {
		return "<nil>"
	}
	s := strings.ReplaceAll(err.Error(), "\n", " | ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}

func goroutines() int { return runtime.NumGoroutine() }

func fdCount() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(ents)
}

// fdBreakdown classifies open descriptors so residue can be attributed instead
// of only counted. Each fd symlink is read; the target is reduced to a class.
func fdBreakdown() string {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return "unavailable"
	}
	counts := map[string]int{}
	for _, e := range ents {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil {
			continue
		}
		class := target
		switch {
		case strings.HasPrefix(target, "socket:"):
			class = "socket"
		case strings.HasPrefix(target, "pipe:"):
			class = "pipe"
		case strings.HasPrefix(target, "anon_inode:"):
			class = "anon_inode"
		case strings.HasPrefix(target, "/dev/"):
			class = "dev"
		case strings.HasPrefix(target, "/tmp/"):
			class = "tmp-file"
		}
		counts[class]++
	}
	return fmt.Sprintf("%v", counts)
}

func socketFDs() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.HasPrefix(target, "socket:") {
			n++
		}
	}
	return n
}

type resourceSnapshot struct {
	label string
	gor   int
	fds   int
	at    time.Time
}

func snap(label string) resourceSnapshot {
	return resourceSnapshot{label: label, gor: goroutines(), fds: fdCount(), at: time.Now()}
}

func (r *reporter) snapshot(s resourceSnapshot) {
	r.kv("resources "+s.label, fmt.Sprintf("goroutines=%d fds=%d", s.gor, s.fds))
}

// freePort reserves a loopback port and releases it. A small race is accepted
// because every peer in this spike is a loopback listener created immediately
// afterwards by the same test process.
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func moduleVersions() []string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	out := []string{"go=" + info.GoVersion, "main=" + info.Main.Path + "@" + info.Main.Version}
	for _, d := range info.Deps {
		if strings.Contains(d.Path, "xray-core") || strings.Contains(d.Path, "refraction-networking") {
			out = append(out, d.Path+"@"+d.Version)
		}
	}
	return out
}
