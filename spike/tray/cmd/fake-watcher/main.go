// Command fake-watcher is a minimal StatusNotifierWatcher stand-in used only
// to exercise the "watcher present, host absent" branch on an isolated bus.
// It draws nothing and touches no desktop state.
package main

import (
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"routelatch/spike/tray/internal/spike"
)

type watcher struct {
	mu    sync.Mutex
	items []string
	props *prop.Properties
}

func (w *watcher) RegisterStatusNotifierItem(service string, sender dbus.Sender) *dbus.Error {
	item := service
	if len(service) == 0 || service[0] != '/' {
		item = service + "/StatusNotifierItem"
	}
	w.mu.Lock()
	w.items = append(w.items, item)
	items := append([]string(nil), w.items...)
	w.mu.Unlock()
	if w.props != nil {
		w.props.SetMust("org.kde.StatusNotifierWatcher", "RegisteredStatusNotifierItems", items)
	}
	return nil
}

func (w *watcher) RegisterStatusNotifierHost(service string) *dbus.Error { return nil }

func main() {
	host := flag.Bool("host", true, "report a registered StatusNotifierHost")
	flag.Parse()

	conn, err := spike.ConnectSession()
	if err != nil {
		spike.Log("event=error stage=connect err=%q", err)
		os.Exit(1)
	}
	defer conn.Close()

	reply, err := conn.RequestName("org.kde.StatusNotifierWatcher", dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		spike.Log("event=error stage=request_name reply=%v err=%v", reply, err)
		os.Exit(1)
	}
	w := &watcher{}
	if err := conn.Export(w, "/StatusNotifierWatcher", "org.kde.StatusNotifierWatcher"); err != nil {
		spike.Log("event=error stage=export_methods err=%q", err)
		os.Exit(1)
	}
	props, err := prop.Export(conn, "/StatusNotifierWatcher", prop.Map{
		"org.kde.StatusNotifierWatcher": {
			"RegisteredStatusNotifierItems":  {Value: []string{}, Writable: false, Emit: prop.EmitTrue},
			"IsStatusNotifierHostRegistered": {Value: *host, Writable: false, Emit: prop.EmitTrue},
			"ProtocolVersion":                {Value: int32(0), Writable: false, Emit: prop.EmitConst},
		},
	})
	if err != nil {
		spike.Log("event=error stage=export_props err=%q", err)
		os.Exit(1)
	}
	w.props = props
	spike.Log("event=fake_watcher_ready host=%v", *host)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
