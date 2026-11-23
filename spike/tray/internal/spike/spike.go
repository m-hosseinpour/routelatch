// Package spike holds shared helpers for the tray feasibility spike only.
//
// It is not product code: it exists to compare StatusNotifierItem libraries
// and to measure the lifecycle guarantees the future `run` supervisor needs.
package spike

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// AppID is the unique identifier used by every prototype so that a stray
// process is easy to find and the real desktop's tray items are untouched.
const AppID = "routelatch-tray-spike"

// WatcherNames lists the SNI watcher bus names in probe order. KDE Plasma 6
// exposes only org.kde.StatusNotifierWatcher; the freedesktop name is checked
// because some implementations provide it.
var WatcherNames = []string{
	"org.kde.StatusNotifierWatcher",
	"org.freedesktop.StatusNotifierWatcher",
}

// IconImage returns the in-memory 22x22 prototype icon.
func IconImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 22, 22))
	for y := 0; y < 22; y++ {
		for x := 0; x < 22; x++ {
			dx, dy := x-11, y-11
			if dx*dx+dy*dy <= 100 {
				img.Set(x, y, color.RGBA{R: 0x2f, G: 0x81, B: 0xf7, A: 0xff})
			}
		}
	}
	// White latch bar so the icon is visually distinguishable when a human
	// looks at it during the live KDE check.
	for y := 8; y < 14; y++ {
		for x := 7; x < 15; x++ {
			img.Set(x, y, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
		}
	}
	return img
}

// IconPNG encodes the prototype icon as PNG bytes.
func IconPNG() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, IconImage()); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// ConnectSession opens the session bus.
func ConnectSession() (*dbus.Conn, error) {
	return dbus.ConnectSessionBus()
}

// FindWatcher returns the first present SNI watcher bus name. An empty string
// with nil error means the bus is reachable but no watcher owns either name,
// which is the explicit "no tray host" case.
func FindWatcher(conn *dbus.Conn) (string, error) {
	obj := conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	for _, name := range WatcherNames {
		var owned bool
		if err := obj.Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&owned); err != nil {
			return "", fmt.Errorf("NameHasOwner(%s): %w", name, err)
		}
		if owned {
			return name, nil
		}
	}
	return "", nil
}

// watcherProperty reads one watcher property. The bus name is not necessarily
// the interface name (they coincide for the two names probed here), so both
// conventional interfaces are tried before reporting failure.
func watcherProperty(conn *dbus.Conn, watcher, property string, out any) error {
	ifaces := []string{watcher}
	for _, fallback := range []string{"org.kde.StatusNotifierWatcher", "org.freedesktop.StatusNotifierWatcher"} {
		if fallback != watcher {
			ifaces = append(ifaces, fallback)
		}
	}
	var lastErr error
	for _, iface := range ifaces {
		err := conn.Object(watcher, "/StatusNotifierWatcher").Call(
			"org.freedesktop.DBus.Properties.Get", 0, iface, property,
		).Store(out)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// RegisteredItems reads the watcher's RegisteredStatusNotifierItems property.
func RegisteredItems(conn *dbus.Conn, watcher string) ([]string, error) {
	var items []string
	if err := watcherProperty(conn, watcher, "RegisteredStatusNotifierItems", &items); err != nil {
		return nil, fmt.Errorf("get RegisteredStatusNotifierItems: %w", err)
	}
	return items, nil
}

// HostRegistered reports whether the watcher says a StatusNotifierHost (the
// panel that actually draws icons) is registered. A watcher without a host is
// exactly the "tray exists on the bus but nothing displays it" case.
func HostRegistered(conn *dbus.Conn, watcher string) (bool, error) {
	var host bool
	if err := watcherProperty(conn, watcher, "IsStatusNotifierHostRegistered", &host); err != nil {
		return false, fmt.Errorf("get IsStatusNotifierHostRegistered: %w", err)
	}
	return host, nil
}

// WaitRegistered polls the watcher until an item belonging to pid appears.
// It returns the matching entry (for example
// "org.kde.StatusNotifierItem-1234-1/StatusNotifierItem").
func WaitRegistered(conn *dbus.Conn, watcher string, pid int, timeout time.Duration) (string, error) {
	needle := fmt.Sprintf("-%d-", pid)
	deadline := time.Now().Add(timeout)
	var last []string
	for {
		items, err := RegisteredItems(conn, watcher)
		if err == nil {
			last = items
			for _, item := range items {
				if bytes.Contains([]byte(item), []byte(needle)) {
					return item, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no watcher item for pid %d within %s; watcher items: %v", pid, timeout, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Notify sends one desktop notification through org.freedesktop.Notifications
// and returns the server-assigned notification id.
func Notify(conn *dbus.Conn, appName, summary, body string) (uint32, error) {
	var id uint32
	err := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
		"org.freedesktop.Notifications.Notify", 0,
		appName, uint32(0), "", summary, body,
		[]string{}, map[string]dbus.Variant{
			"desktop-entry": dbus.MakeVariant(AppID),
		}, int32(-1),
	).Store(&id)
	if err != nil {
		return 0, fmt.Errorf("Notify: %w", err)
	}
	return id, nil
}

// ErrParentGone reports that the supervising parent was already gone when the
// child armed its watchdog. It is not a setup failure: the guarantee held.
var ErrParentGone = errors.New("supervising parent already exited")

// The supervisor can hand the child a pidfd opened on itself, which closes the
// arm-late race: a pidfd inherited through ExtraFiles is readable even if the
// supervisor exits before the child calls ArmParentDeath.
const (
	ParentPIDFDEnv = "RL_SPIKE_PARENT_PIDFD" // child fd number
	ParentPIDEnv   = "RL_SPIKE_PARENT_PID"   // expected supervisor pid
)

// ArmParentDeath returns a descriptor that is readable when the supervising
// parent dies. It prefers a pidfd inherited from the supervisor; without one it
// opens a pidfd on the current parent. In both cases it compares the parent pid
// with the expected supervisor pid and reports ErrParentGone instead of
// silently watching an unrelated process.
func ArmParentDeath() (int, error) {
	expected := 0
	if s := os.Getenv(ParentPIDEnv); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 1 {
			return -1, fmt.Errorf("invalid %s=%q", ParentPIDEnv, s)
		}
		expected = n
	}

	if s := os.Getenv(ParentPIDFDEnv); s != "" {
		fd, err := strconv.Atoi(s)
		if err != nil || fd < 3 {
			return -1, fmt.Errorf("invalid %s=%q", ParentPIDFDEnv, s)
		}
		// The supervisor opened this descriptor on itself, so it is readable
		// even if the supervisor died before this point; polling it cannot
		// silently target a later process that reused the pid.
		return fd, nil
	}

	ppid := os.Getppid()
	if ppid <= 1 || (expected > 0 && ppid != expected) {
		return -1, ErrParentGone
	}
	fd, err := unix.PidfdOpen(ppid, 0)
	if err != nil {
		return -1, fmt.Errorf("pidfd_open(%d): %w", ppid, err)
	}
	if got := os.Getppid(); got != ppid {
		unix.Close(fd)
		return -1, ErrParentGone
	}
	return fd, nil
}

// WaitParentDeath blocks until the pidfd opened by ArmParentDeath is readable.
func WaitParentDeath(fd int) error {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("poll pidfd: %w", err)
		}
		if n > 0 {
			return nil
		}
	}
}

// Log emits one machine-readable evidence line. Timestamps are UTC with
// milliseconds so a parent-death measurement can be reconstructed from logs.
func Log(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s "+format+"\n",
		append([]any{time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, args...)...)
}
