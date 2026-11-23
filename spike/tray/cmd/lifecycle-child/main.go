// Command lifecycle-child is the tray-process stand-in for parent-death and
// exit-detection measurements. It touches no D-Bus and no desktop state.
package main

import (
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"routelatch/spike/tray/internal/spike"
)

func main() {
	seconds := flag.Int("seconds", 0, "exit on its own after this many seconds (0 = wait forever)")
	watchParent := flag.Bool("watch-parent", false, "exit when the supervisor process dies (pidfd)")
	armDelay := flag.Duration("arm-delay", 0, "wait this long before arming the parent watchdog (race test)")
	flag.Parse()

	spike.Log("event=child_ready pid=%d ppid=%d", os.Getpid(), os.Getppid())

	if *watchParent {
		if *armDelay > 0 {
			time.Sleep(*armDelay)
		}
		fd, err := spike.ArmParentDeath()
		if errors.Is(err, spike.ErrParentGone) {
			spike.Log("event=parent_died exit=watchdog reason=already_gone_before_arm")
			os.Exit(0)
		}
		if err != nil {
			spike.Log("event=parent_watch_error err=%q", err)
			os.Exit(7)
		}
		spike.Log("event=parent_watch_armed pidfd=%d ppid=%d", fd, os.Getppid())
		go func() {
			if err := spike.WaitParentDeath(fd); err != nil {
				spike.Log("event=parent_watch_error err=%q", err)
				os.Exit(7)
			}
			spike.Log("event=parent_died exit=watchdog")
			os.Exit(0)
		}()
	}

	var selfTimer <-chan time.Time
	if *seconds > 0 {
		selfTimer = time.After(time.Duration(*seconds) * time.Second)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	select {
	case <-selfTimer:
		spike.Log("event=child_self_exit reason=timer")
		os.Exit(0)
	case s := <-sig:
		spike.Log("event=child_self_exit reason=signal signal=%s", s)
		os.Exit(0)
	}
}
