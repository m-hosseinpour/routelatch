// Command tray-spike runs one deedles.dev/tray StatusNotifierItem prototype.
package main

import (
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"deedles.dev/tray"
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
		// deedles.dev/tray also returns an error from New() in this case; the
		// probe keeps the exit code and message uniform across candidates.
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

	item, err := tray.New(
		tray.ItemID(*id),
		tray.ItemTitle("RouteLatch tray spike"),
		tray.ItemIconPixmap(tray.ToPixmap(spike.IconImage())),
		tray.ItemToolTip("", nil, "RouteLatch tray spike", "pure-Go tray feasibility spike"),
		tray.ItemIsMenu(true),
	)
	if err != nil {
		spike.Log("event=error stage=new err=%q", err)
		os.Exit(1)
	}
	defer item.Close()

	if _, err := item.Menu().AddChild(
		tray.MenuItemLabel("Send test notification"),
		tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
			spike.Log("event=menu_click item=notify")
			id, err := spike.Notify(conn, *id, "RouteLatch spike", "deedles.dev/tray + godbus notification path")
			spike.Log("event=notification_result id=%d err=%v", id, err)
			return nil
		})),
	); err != nil {
		spike.Log("event=error stage=menu_notify err=%q", err)
		os.Exit(1)
	}
	if _, err := item.Menu().AddChild(tray.MenuItemType(tray.Separator)); err != nil {
		spike.Log("event=error stage=menu_separator err=%q", err)
		os.Exit(1)
	}
	if _, err := item.Menu().AddChild(
		tray.MenuItemLabel("Quit prototype"),
		tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
			spike.Log("event=menu_click item=quit")
			item.Close()
			os.Exit(0)
			return nil
		})),
	); err != nil {
		spike.Log("event=error stage=menu_quit err=%q", err)
		os.Exit(1)
	}

	registered, err := spike.WaitRegistered(conn, watcher, os.Getpid(), 3*time.Second)
	if err != nil {
		spike.Log("event=error stage=registration err=%q", err)
		os.Exit(4)
	}
	spike.Log("event=tray_registered impl=deedles.dev/tray id=%s item=%s", *id, registered)

	if *notify {
		time.Sleep(300 * time.Millisecond)
		nid, err := spike.Notify(conn, *id, "RouteLatch spike", "deedles.dev/tray + godbus notification path")
		spike.Log("event=notification_result id=%d err=%v", nid, err)
	}

	if *watchParent {
		fd, err := spike.ArmParentDeath()
		if errors.Is(err, spike.ErrParentGone) {
			spike.Log("event=parent_died exit=watchdog reason=already_gone_before_arm")
			item.Close()
			os.Exit(0)
		}
		if err != nil {
			spike.Log("event=parent_watch_error err=%q", err)
			os.Exit(7)
		}
		spike.Log("event=parent_watch_armed pidfd=%d", fd)
		go func() {
			if err := spike.WaitParentDeath(fd); err != nil {
				spike.Log("event=parent_watch_error err=%q", err)
				os.Exit(7)
			}
			spike.Log("event=parent_died exit=watchdog")
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
		spike.Log("event=exit reason=timer")
	case s := <-sig:
		spike.Log("event=exit reason=signal signal=%s", s)
	}
	item.Close()
}
