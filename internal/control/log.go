package control

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Log rotation bounds from the product contract: a 4 MiB active file and three
// rotated files (routelatch.log.1 ... .3). The log is append-only and 0600.
const (
	logMaxBytes  int64 = 4 << 20
	logRotations       = 3
)

type logLevel int

const (
	levelDebug logLevel = iota
	levelInfo
	levelWarning
	levelError
	levelNone
)

func parseLogLevel(raw string) logLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return levelDebug
	case "info", "":
		return levelInfo
	case "warning", "warn":
		return levelWarning
	case "error":
		return levelError
	case "none", "off":
		return levelNone
	default:
		return levelInfo
	}
}

func (l logLevel) String() string {
	switch l {
	case levelDebug:
		return "debug"
	case levelInfo:
		return "info"
	case levelWarning:
		return "warning"
	case levelError:
		return "error"
	default:
		return "none"
	}
}

// serviceLogger writes bounded, sanitized operational lines to the documented
// log file and, for foreground use, to the service's stdout. Callers pass only
// ids, counts, modes and redacted messages; a URL, credential, request body or
// query string never reaches this type.
type serviceLogger struct {
	mu        sync.Mutex
	path      string
	level     logLevel
	out       io.Writer
	maxBytes  int64
	rotations int
	file      *os.File
	size      int64
}

func newServiceLogger(path, level string, out io.Writer) (*serviceLogger, error) {
	return newServiceLoggerWithLimit(path, level, out, logMaxBytes, logRotations)
}

func newServiceLoggerWithLimit(path, level string, out io.Writer, maxBytes int64, rotations int) (*serviceLogger, error) {
	if maxBytes <= 0 {
		maxBytes = logMaxBytes
	}
	if rotations < 0 {
		rotations = 0
	}
	l := &serviceLogger{
		path: path, level: parseLogLevel(level), out: out,
		maxBytes: maxBytes, rotations: rotations,
	}
	if err := l.open(); err != nil {
		return l, err
	}
	return l, nil
}

func (l *serviceLogger) open() error {
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create the log directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("cannot set log directory permissions: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open the log file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot set log file permissions: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot inspect the log file: %w", err)
	}
	l.mu.Lock()
	l.file = f
	l.size = info.Size()
	l.mu.Unlock()
	return nil
}

func (l *serviceLogger) Path() string { return l.path }

func (l *serviceLogger) SetLevel(raw string) {
	l.mu.Lock()
	l.level = parseLogLevel(raw)
	l.mu.Unlock()
}

// Logf writes one line when level is enabled. It sanitizes the message so a
// node name or engine error cannot inject terminal controls or extra lines.
func (l *serviceLogger) Logf(level logLevel, op, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.level == levelNone || level < l.level {
		return
	}
	msg := SanitizeTerminal(fmt.Sprintf(format, args...))
	if len([]rune(msg)) > 2048 {
		msg = string([]rune(msg)[:2048])
	}
	line := fmt.Sprintf("%s %-7s %s: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), level.String(), op, msg)
	if l.out != nil {
		_, _ = io.WriteString(l.out, line)
	}
	if l.file == nil {
		return
	}
	if l.size > 0 && l.size+int64(len(line)) > l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			// A failed rotation must not kill the service; report it on the
			// foreground stream and keep appending to the current file.
			if l.out != nil {
				_, _ = fmt.Fprintf(l.out, "warning log.rotate: %s\n", SanitizeTerminal(err.Error()))
			}
		}
	}
	n, err := io.WriteString(l.file, line)
	if err == nil {
		l.size += int64(n)
	}
}

func (l *serviceLogger) rotateLocked() error {
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			return err
		}
		l.file = nil
	}
	oldest := fmt.Sprintf("%s.%d", l.path, l.rotations)
	if l.rotations > 0 {
		_ = os.Remove(oldest)
	}
	for i := l.rotations - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", l.path, i)
		to := fmt.Sprintf("%s.%d", l.path, i+1)
		_ = os.Rename(from, to)
	}
	if l.rotations > 0 {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	l.file = f
	l.size = 0
	return nil
}

// Close flushes nothing (writes are direct) and releases the file.
func (l *serviceLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// logFileWritable reports whether the service could append to path. It is used
// by doctor and never creates data when the file is absent.
func logFileWritable(path string) error {
	if info, err := os.Stat(path); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("mode is %04o, expected 0600", info.Mode().Perm())
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("not writable: %w", err)
		}
		return f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("log directory is missing")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("log directory mode is %04o, expected 0700", info.Mode().Perm())
	}
	f, err := os.OpenFile(filepath.Join(dir, ".routelatch-log-probe"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("log directory is not writable: %w", err)
	}
	_ = f.Close()
	_ = os.Remove(filepath.Join(dir, ".routelatch-log-probe"))
	return nil
}
