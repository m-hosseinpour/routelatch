// Command menu-spike exercises the dbusmenu capabilities the R15 tray control
// surface needs: nested submenus, separators, radio/checkmark state,
// visible/enabled toggles, runtime add/remove/relabel, a 20-item page from a
// 176-node pool fixture, and long/Unicode labels.
//
// It is evidence-only spike code. The application-level model lives in
// menu.go; this file only owns process setup, the item, and the private
// control interface used by the evidence scripts to drive menu mutations.
package main

import (
	"errors"
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"deedles.dev/tray"
	"github.com/godbus/dbus/v5"
	"routelatch/spike/tray/internal/spike"
)

// menuAppID is deliberately distinct from AppID (R13 spikes) and from the
// installed product's running tray item, so evidence scripts can select this
// item unambiguously on the live session bus.
const menuAppID = "routelatch-tray-menu-spike"

var logMu sync.Mutex

func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	spike.Log(format, args...)
}

// control is exported on the item's unique bus name at /Control. It exists
// only so the evidence driver can request deterministic menu mutations at a
// known point in time; it is not part of any proposed product interface.
type control struct {
	menu *spikeMenu
}

func (c *control) Apply(stage string) (string, *dbus.Error) {
	summary, err := c.menu.apply(stage)
	if err != nil {
		logf("event=control_apply stage=%q ok=false err=%q", stage, err)
		return summary, dbus.MakeFailedError(err)
	}
	logf("event=control_apply stage=%q ok=true summary=%q", stage, summary)
	return summary, nil
}

func main() {
	id := flag.String("id", menuAppID, "StatusNotifierItem Id")
	seconds := flag.Int("seconds", 0, "exit after this many seconds (0 = until signal)")
	watchParent := flag.Bool("watch-parent", false, "exit when the supervising parent dies (pidfd)")
	openFlag := flag.Bool("open-test", false, "log that this run is intended for a real open/menu interaction")
	flag.Parse()

	conn, err := spike.ConnectSession()
	if err != nil {
		logf("event=error stage=connect err=%q", err)
		os.Exit(1)
	}
	defer conn.Close()

	watcher, err := spike.FindWatcher(conn)
	if err != nil {
		logf("event=error stage=watcher_probe err=%q", err)
		os.Exit(1)
	}
	if watcher == "" {
		logf("event=no_watcher stage=probe names=%v", spike.WatcherNames)
		os.Exit(3)
	}
	logf("event=watcher_found watcher=%s", watcher)
	host, err := spike.HostRegistered(conn, watcher)
	if err != nil {
		logf("event=error stage=host_probe err=%q", err)
		os.Exit(1)
	}
	logf("event=host_registered host=%v", host)
	if !host {
		logf("event=no_tray_host watcher=%s", watcher)
		os.Exit(5)
	}

	menuState := newSpikeMenu()
	item, err := tray.New(
		tray.ItemID(*id),
		tray.ItemTitle("RouteLatch menu spike"),
		tray.ItemIconPixmap(tray.ToPixmap(menuIconImage())),
		tray.ItemToolTip("", nil, "RouteLatch menu spike", "R15-A dbusmenu capability evidence"),
		tray.ItemIsMenu(true),
		tray.ItemMenuHandler(menuState.handleMenuEvent),
	)
	if err != nil {
		logf("event=error stage=new err=%q", err)
		os.Exit(1)
	}
	defer item.Close()
	menuState.bind(item)

	if err := menuState.build(); err != nil {
		logf("event=error stage=build_menu err=%q", err)
		os.Exit(1)
	}
	logf("event=menu_built stages=%v", menuState.stages())
	if *openFlag {
		logf("event=open_test requested=true icon=magenta")
	}

	ctl := &control{menu: menuState}
	if err := conn.Export(ctl, "/Control", "dev.routelatch.MenuSpike"); err != nil {
		logf("event=error stage=export_control err=%q", err)
		os.Exit(1)
	}
	// The item owns its own private connection inside tray.New, so the control
	// object lives on this second connection and is addressed by its unique
	// bus name. Log it for the evidence driver.
	controlBus := conn.Names()[0]
	logf("event=control_ready bus=%s path=/Control interface=dev.routelatch.MenuSpike", controlBus)

	registered, err := spike.WaitRegistered(conn, watcher, os.Getpid(), 3*time.Second)
	if err != nil {
		logf("event=error stage=registration err=%q", err)
		os.Exit(4)
	}
	logf("event=tray_registered id=%s item=%s", *id, registered)

	if *watchParent {
		fd, err := spike.ArmParentDeath()
		if errors.Is(err, spike.ErrParentGone) {
			logf("event=parent_died exit=watchdog reason=already_gone_before_arm")
			item.Close()
			os.Exit(0)
		}
		if err != nil {
			logf("event=parent_watch_error err=%q", err)
			os.Exit(7)
		}
		logf("event=parent_watch_armed pidfd=%d", fd)
		go func() {
			if err := spike.WaitParentDeath(fd); err != nil {
				logf("event=parent_watch_error err=%q", err)
				os.Exit(7)
			}
			logf("event=parent_died exit=watchdog")
			item.Close()
			os.Exit(0)
		}()
	}

	var timeout <-chan time.Time
	if *seconds > 0 {
		timeout = time.After(time.Duration(*seconds) * time.Second)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	select {
	case <-timeout:
		logf("event=exit reason=timer")
	case s := <-sig:
		logf("event=exit reason=signal signal=%s", s)
	}
	item.Close()
}

// waitClickDone is used only by the quit handler so the D-Bus Event reply can
// be sent before the process exits; the library writes the reply after the
// handler returns.
func waitClickDone() {
	time.Sleep(20 * time.Millisecond)
	os.Exit(0)
}
