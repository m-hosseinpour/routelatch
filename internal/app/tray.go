package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"deedles.dev/tray"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// WatcherNames lists the StatusNotifierWatcher bus names in probe order. KDE
// exposes the first; some implementations provide the second.
var WatcherNames = []string{
	"org.kde.StatusNotifierWatcher",
	"org.freedesktop.StatusNotifierWatcher",
}

// trayProbeTimeout bounds ProbeTray and the supervisor/registration checks so
// a wedged session bus cannot hold app preflight, autostart or doctor.
const trayProbeTimeout = 2 * time.Second

// ProbeTray verifies that a StatusNotifier watcher exists and that a host is
// registered, with a bounded deadline. The app owns this probe: the tray
// library does not provide a readiness signal, and D26 needs it for
// status.ready and tray-loss handling.
func ProbeTray() error {
	ctx, cancel := context.WithTimeout(context.Background(), trayProbeTimeout)
	defer cancel()
	return ProbeTrayContext(ctx)
}

// ProbeTrayContext is the context-aware tray probe. Cancellation aborts the
// in-flight bus calls; no deadline is imposed here, so callers choose the bound.
func ProbeTrayContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := connectSessionBus(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	watcher, err := findWatcher(ctx, conn)
	if err != nil {
		return err
	}
	if watcher == "" {
		return errors.New("no StatusNotifierWatcher on the session bus")
	}
	host, err := hostRegistered(ctx, conn, watcher)
	if err != nil {
		return err
	}
	if !host {
		return errors.New("the StatusNotifierWatcher has no registered host")
	}
	return nil
}

// connectSessionBus returns an authenticated private session-bus connection
// without autolaunch. godbus's Auth and Hello take no context and do blocking
// socket I/O, so the connection is closed when ctx is done; otherwise a peer
// that accepts but stalls mid-handshake would hang the probe past its deadline.
func connectSessionBus(ctx context.Context) (*dbus.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// No autolaunch: a probe must not start a session bus just to discover
	// that it has no host. A desktop session already has DBUS_SESSION_BUS_ADDRESS
	// or /run/user/<uid>/bus; anywhere else there is no tray to find.
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, fmt.Errorf("session bus is unavailable: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	// Private connections are unauthenticated by construction: calls on them
	// without Auth/Hello never complete, which made the old probe hang on a
	// live session bus.
	err = conn.Auth(nil)
	if err == nil {
		err = conn.Hello()
	}
	stop()
	if err != nil {
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("session bus handshake failed: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = conn.Close()
		return nil, ctxErr
	}
	return conn, nil
}

func findWatcher(ctx context.Context, conn *dbus.Conn) (string, error) {
	obj := conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	for _, name := range WatcherNames {
		var owned bool
		if err := obj.CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&owned); err != nil {
			return "", fmt.Errorf("NameHasOwner(%s): %w", name, err)
		}
		if owned {
			return name, nil
		}
	}
	return "", nil
}

func watcherProperty(ctx context.Context, conn *dbus.Conn, watcher, property string, out any) error {
	ifaces := []string{watcher}
	for _, fallback := range []string{"org.kde.StatusNotifierWatcher", "org.freedesktop.StatusNotifierWatcher"} {
		if fallback != watcher {
			ifaces = append(ifaces, fallback)
		}
	}
	var lastErr error
	for _, iface := range ifaces {
		err := conn.Object(watcher, "/StatusNotifierWatcher").CallWithContext(
			ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, property,
		).Store(out)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

func hostRegistered(ctx context.Context, conn *dbus.Conn, watcher string) (bool, error) {
	var host bool
	if err := watcherProperty(ctx, conn, watcher, "IsStatusNotifierHostRegistered", &host); err != nil {
		return false, fmt.Errorf("get IsStatusNotifierHostRegistered: %w", err)
	}
	return host, nil
}

func registeredItems(ctx context.Context, conn *dbus.Conn, watcher string) ([]string, error) {
	var items []string
	if err := watcherProperty(ctx, conn, watcher, "RegisteredStatusNotifierItems", &items); err != nil {
		return nil, fmt.Errorf("get RegisteredStatusNotifierItems: %w", err)
	}
	return items, nil
}

func itemRegistered(items []string, pid int) bool {
	needle := fmt.Sprintf("-%d-", pid)
	for _, item := range items {
		if contains(item, needle) {
			return true
		}
	}
	return false
}

func waitRegistered(ctx context.Context, conn *dbus.Conn, watcher string, pid int, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		items, err := registeredItems(waitCtx, conn, watcher)
		if err == nil && itemRegistered(items, pid) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("the tray item for pid %d did not register within %s: %w", pid, timeout, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Notify sends one destination-free desktop notification.
func Notify(conn *dbus.Conn, summary, body string) error {
	if conn == nil {
		return errors.New("no session bus")
	}
	var id uint32
	return conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
		"org.freedesktop.Notifications.Notify", 0,
		"RouteLatch", uint32(0), "", summary, body,
		[]string{}, map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("routelatch")}, int32(-1),
	).Store(&id)
}

// TrayRegistered verifies that this app's tray item is still registered with
// the current watcher using a bounded deadline.
func TrayRegistered(pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), trayProbeTimeout)
	defer cancel()
	return TrayRegisteredContext(ctx, pid)
}

// TrayRegisteredContext is the context-aware registration check. It closes
// over a fresh connection so it does not depend on the tray child's own bus
// handle.
func TrayRegisteredContext(ctx context.Context, pid int) error {
	conn, err := connectSessionBus(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	watcher, err := findWatcher(ctx, conn)
	if err != nil {
		return err
	}
	if watcher == "" {
		return errors.New("no StatusNotifierWatcher")
	}
	items, err := registeredItems(ctx, conn, watcher)
	if err != nil {
		return err
	}
	if !itemRegistered(items, pid) {
		return fmt.Errorf("tray item for pid %d is not registered", pid)
	}
	return nil
}

// TrayOptions configures the standalone tray child.
type TrayOptions struct {
	ControlFD   int
	ParentFD    int
	ExpectedPID int
	LogPath     string
	ReadyWait   time.Duration
}

// RunTray is the tray child: one StatusNotifierItem, one godbus notification
// path and a control pipe to the supervisor. It never retries tray.New on a
// missing watcher; the supervisor restarts the process instead.
func RunTray(opts TrayOptions) error {
	if opts.ReadyWait <= 0 {
		opts.ReadyWait = 3 * time.Second
	}
	controlFile := os.NewFile(uintptr(opts.ControlFD), "tray-control")
	if controlFile == nil {
		return internalFailure("app.tray", "tray control descriptor is invalid", nil)
	}
	defer controlFile.Close()
	writer := bufio.NewWriter(controlFile)
	send := func(line string) {
		_, _ = writer.WriteString(line + "\n")
		_ = writer.Flush()
	}

	parentGone, err := watchParent(opts.ParentFD, opts.ExpectedPID)
	if err != nil {
		send("error parent_watch_failed")
		return internalFailure("app.tray", "cannot watch the supervisor pidfd", err)
	}
	select {
	case <-parentGone:
		return nil
	default:
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		send("error tray_unavailable")
		return unsupportedTray(err)
	}
	defer conn.Close()
	watcher, err := findWatcher(context.Background(), conn)
	if err != nil || watcher == "" {
		send("error tray_unavailable")
		return unsupportedTray(firstErr(err, errors.New("no StatusNotifierWatcher")))
	}
	host, err := hostRegistered(context.Background(), conn, watcher)
	if err != nil || !host {
		send("error tray_unavailable")
		return unsupportedTray(firstErr(err, errors.New("no StatusNotifierHost")))
	}

	item, err := tray.New(
		tray.ItemID("routelatch"),
		tray.ItemTitle("RouteLatch"),
		tray.ItemIconPixmap(trayIcon()),
		tray.ItemToolTip("", nil, "RouteLatch", "local proxy with an approved-destination list"),
		tray.ItemIsMenu(true),
	)
	if err != nil {
		send("error tray_unavailable")
		return unsupportedTray(err)
	}
	var closeOnce sync.Once
	closeItem := func() {
		closeOnce.Do(func() { _ = item.Close() })
	}
	defer closeItem()

	quit := make(chan struct{}, 1)
	if _, err := item.Menu().AddChild(
		tray.MenuItemLabel("Quit RouteLatch"),
		tray.MenuItemHandler(tray.ClickedHandler(func(any, uint32) error {
			select {
			case quit <- struct{}{}:
			default:
			}
			return nil
		})),
	); err != nil {
		send("error tray_menu_failed")
		return internalFailure("app.tray", "cannot add the tray Quit entry", err)
	}
	if _, err := item.Menu().AddChild(
		tray.MenuItemLabel("Show log path"),
		tray.MenuItemHandler(tray.ClickedHandler(func(any, uint32) error {
			_ = Notify(conn, "RouteLatch log", control.SanitizeTerminal(opts.LogPath))
			return nil
		})),
	); err != nil {
		send("error tray_menu_failed")
		return internalFailure("app.tray", "cannot add the tray log entry", err)
	}

	if err := waitRegistered(context.Background(), conn, watcher, os.Getpid(), opts.ReadyWait); err != nil {
		send("error tray_registration_failed")
		return internalFailure("app.tray", "the tray item did not register", err)
	}
	send("ready")
	_ = Notify(conn, "RouteLatch is running", "Tray icon is ready. Log: "+control.SanitizeTerminal(opts.LogPath))

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	monitor := time.NewTicker(2 * time.Second)
	defer monitor.Stop()
	for {
		select {
		case <-parentGone:
			closeItem()
			return nil
		case <-quit:
			send("quit")
			closeItem()
			return nil
		case <-sigCh:
			closeItem()
			return nil
		case <-monitor.C:
			watcher, err := findWatcher(context.Background(), conn)
			if err != nil || watcher == "" {
				send("lost")
				return &Failure{Exit: 3, Kind: contract.Internal, Op: "app.tray", Message: "the StatusNotifierWatcher disappeared"}
			}
			host, err := hostRegistered(context.Background(), conn, watcher)
			if err != nil || !host {
				send("lost")
				return &Failure{Exit: 3, Kind: contract.Internal, Op: "app.tray", Message: "the StatusNotifierHost disappeared"}
			}
			items, err := registeredItems(context.Background(), conn, watcher)
			if err != nil || !itemRegistered(items, os.Getpid()) {
				send("lost")
				return &Failure{Exit: 3, Kind: contract.Internal, Op: "app.tray", Message: "the tray item is no longer registered"}
			}
		}
	}
}

// watchParent returns a channel that closes when the supervisor pidfd becomes
// readable. Without an inherited pidfd it falls back to the current parent,
// but app mode always supplies one.
func watchParent(fd, expected int) (<-chan struct{}, error) {
	gone := make(chan struct{})
	if fd >= 3 {
		f := os.NewFile(uintptr(fd), "supervisor-pidfd")
		if f == nil {
			return nil, errors.New("invalid supervisor pidfd")
		}
		go func() {
			defer f.Close()
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			for {
				n, err := unix.Poll(poll, -1)
				if err == unix.EINTR {
					continue
				}
				if err != nil || n > 0 {
					close(gone)
					return
				}
			}
		}()
		return gone, nil
	}
	ppid := os.Getppid()
	if ppid <= 1 || (expected > 0 && ppid != expected) {
		close(gone)
		return gone, nil
	}
	fd, err := unix.PidfdOpen(ppid, 0)
	if err != nil {
		return nil, err
	}
	return watchParent(fd, expected)
}

func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func trayIcon() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 22, 22))
	for y := 0; y < 22; y++ {
		for x := 0; x < 22; x++ {
			dx, dy := x-11, y-11
			if dx*dx+dy*dy <= 100 {
				img.Set(x, y, color.RGBA{R: 0x2f, G: 0x81, B: 0xf7, A: 0xff})
			}
		}
	}
	for y := 8; y < 14; y++ {
		for x := 7; x < 15; x++ {
			img.Set(x, y, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
		}
	}
	return img
}

// TrayOptionsFromEnv is the hidden command's environment contract.
func TrayOptionsFromEnv() (TrayOptions, error) {
	control, err := strconv.Atoi(os.Getenv("ROUTELATCH_TRAY_CONTROL_FD"))
	if err != nil || control < 3 {
		return TrayOptions{}, fmt.Errorf("invalid ROUTELATCH_TRAY_CONTROL_FD")
	}
	parent, err := strconv.Atoi(os.Getenv("ROUTELATCH_TRAY_PARENT_PIDFD"))
	if err != nil || parent < 3 {
		return TrayOptions{}, fmt.Errorf("invalid ROUTELATCH_TRAY_PARENT_PIDFD")
	}
	expected, _ := strconv.Atoi(os.Getenv("ROUTELATCH_PARENT_PID"))
	return TrayOptions{
		ControlFD: control, ParentFD: parent, ExpectedPID: expected,
		LogPath: os.Getenv("ROUTELATCH_LOG_PATH"),
	}, nil
}

// ParentWatchFromEnv arms the headless engine's parent-death watchdog. It
// returns a cancel function that stops the watcher and whether this process is
// the app supervisor's own engine child. That exception is not granted from an
// environment marker alone: the inherited pidfd must name this process's real
// parent, and that parent must equal ROUTELATCH_PARENT_PID. A stale or forged
// marker therefore stays an ordinary headless engine.
func ParentWatchFromEnv() (context.Context, context.CancelFunc, bool, error) {
	fdRaw := os.Getenv("ROUTELATCH_PARENT_PIDFD")
	if fdRaw == "" {
		return nil, nil, false, nil
	}
	fd, err := strconv.Atoi(fdRaw)
	if err != nil || fd < 3 {
		return nil, nil, false, fmt.Errorf("invalid ROUTELATCH_PARENT_PIDFD")
	}
	expected := 0
	if expectedRaw := os.Getenv("ROUTELATCH_PARENT_PID"); expectedRaw != "" {
		expected, err = strconv.Atoi(expectedRaw)
		if err != nil || expected <= 1 {
			return nil, nil, false, fmt.Errorf("invalid ROUTELATCH_PARENT_PID")
		}
		// A readable fdinfo target that differs from the advertised parent is
		// a forged/stale inherited descriptor: ignore it entirely. If the
		// supervisor already died, the pidfd still names it, so arming the
		// watch below makes this child exit instead of orphaning it.
		if target := pidfdTargetPID(fd); target != 0 && target != expected {
			return nil, nil, false, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			n, err := unix.Poll(fds, -1)
			if err == unix.EINTR {
				continue
			}
			if err != nil || n > 0 {
				return
			}
		}
	}()
	supervised := os.Getenv("ROUTELATCH_APP_ENGINE") == "1" && expected > 1 && os.Getppid() == expected
	return ctx, cancel, supervised, nil
}

// pidfdTargetPID reads the pid a pidfd was opened on from /proc fdinfo. It
// returns 0 when the descriptor is not a pidfd or cannot be inspected, which
// keeps the caller on the conservative (non-supervised) path.
func pidfdTargetPID(fd int) int {
	raw, err := os.ReadFile(fmt.Sprintf("%s/self/fdinfo/%d", procRoot, fd))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Pid:") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pid:")))
		if err != nil || pid <= 1 {
			return 0
		}
		return pid
	}
	return 0
}
