// Command systray-spike runs one gogpu/systray StatusNotifierItem prototype.
// It never installs anything and uses a unique app id; the only desktop effect
// is one tray icon that disappears when the process exits.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gogpu/systray"
	"routelatch/spike/tray/internal/spike"
)

func main() {
	id := flag.String("id", spike.AppID, "StatusNotifierItem Id")
	seconds := flag.Int("seconds", 0, "exit after this many seconds (0 = until signal)")
	notify := flag.Bool("notify", false, "send one test notification after registration")
	watchParent := flag.Bool("watch-parent", false, "exit when the supervising parent dies (pidfd)")
	checkWatcher := flag.Bool("check-watcher", false, "only probe for an SNI watcher and exit")
	rawCreate := flag.Bool("raw-create", false, "skip the pre-probe and rely on the library's own watcher handling (comparison evidence)")
	flag.Parse()

	conn, err := spike.ConnectSession()
	if err != nil {
		spike.Log("event=error stage=connect err=%q", err)
		os.Exit(1)
	}
	defer conn.Close()

	watcher, err := spike.FindWatcher(conn)
	if err != nil {
		spike.Log("event=error stage=watcher_probe err=%q", err)
		os.Exit(1)
	}
	host := false
	if watcher == "" {
		if !*rawCreate {
			spike.Log("event=no_watcher stage=probe names=%v", spike.WatcherNames)
			os.Exit(3)
		}
		spike.Log("event=no_watcher stage=probe action=continuing_for_raw_create")
	} else {
		spike.Log("event=watcher_found watcher=%s", watcher)
		host, err = spike.HostRegistered(conn, watcher)
		if err != nil {
			spike.Log("event=error stage=host_probe err=%q", err)
			os.Exit(1)
		}
		spike.Log("event=host_registered host=%v", host)
		if !host && !*rawCreate {
			spike.Log("event=no_tray_host watcher=%s", watcher)
			os.Exit(5)
		}
	}
	if *checkWatcher {
		if watcher != "" && !host {
			os.Exit(5)
		}
		os.Exit(0)
	}

	// gogpu/systray Create() treats a failed watcher registration as non-fatal
	// and only logs a warning, so the explicit check above is required for the
	// "no silent success" constraint. This is itself a comparison finding.
	tray := systray.New()
	tray.SetAppName(*id)

	// gogpu/systray's Remove() is not idempotent: a second call panics while
	// setting properties on the already-closed connection. The prototype
	// guards it; a real application must do the same (comparison finding).
	var removeOnce sync.Once
	remove := func() { removeOnce.Do(tray.Remove) }

	menu := systray.NewMenu()
	menu.Add("Send test notification", func() {
		spike.Log("event=menu_click item=notify")
		tray.ShowNotification("RouteLatch spike", "gogpu/systray notification path")
	})
	menu.AddSeparator()
	menu.Add("Quit prototype", func() {
		spike.Log("event=menu_click item=quit")
		go func() { time.Sleep(100 * time.Millisecond); os.Exit(0) }()
		// Removing from inside the menu callback unblocks Run().
		remove()
	})

	tray.SetIcon(spike.IconPNG()).
		SetTooltip("RouteLatch tray spike").
		SetMenu(menu).
		Show()

	if watcher == "" {
		// gogpu/systray's API has no error return from New()/Create(); the
		// failed watcher registration was only logged as a warning.
		spike.Log("event=library_create_reported_no_error impl=gogpu/systray watcher_present=false")
		time.Sleep(time.Second)
		remove()
		os.Exit(3)
	}

	item, err := spike.WaitRegistered(conn, watcher, os.Getpid(), 3*time.Second)
	if err != nil {
		spike.Log("event=error stage=registration err=%q", err)
		remove()
		os.Exit(4)
	}
	spike.Log("event=tray_registered impl=gogpu/systray id=%s item=%s", *id, item)

	if *notify {
		time.Sleep(300 * time.Millisecond)
		tray.ShowNotification("RouteLatch spike", "gogpu/systray notification path")
		spike.Log("event=notification_sent via=gogpu/systray (errors are not observable through this API)")
	}

	if *watchParent {
		fd, err := spike.ArmParentDeath()
		if errors.Is(err, spike.ErrParentGone) {
			spike.Log("event=parent_died exit=watchdog reason=already_gone_before_arm")
			remove()
			os.Exit(0)
		}
		if err != nil {
			spike.Log("event=parent_watch_error err=%q", err)
			remove()
			os.Exit(7)
		}
		spike.Log("event=parent_watch_armed pidfd=%d", fd)
		go func() {
			if err := spike.WaitParentDeath(fd); err != nil {
				spike.Log("event=parent_watch_error err=%q", err)
				os.Exit(7)
			}
			spike.Log("event=parent_died exit=watchdog")
			remove()
			os.Exit(0)
		}()
	}

	runErr := make(chan error, 1)
	go func() { runErr <- tray.Run() }()

	var timeout <-chan time.Time
	if *seconds > 0 {
		timeout = time.After(time.Duration(*seconds) * time.Second)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-runErr:
		spike.Log("event=tray_loop_exit err=%v", err)
	case <-timeout:
		spike.Log("event=exit reason=timer")
	case s := <-sig:
		spike.Log("event=exit reason=signal signal=%s", s)
	}
	remove()
	fmt.Fprintln(os.Stderr, "spike: tray removed")
}
