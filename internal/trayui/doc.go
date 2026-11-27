// Package trayui is the pure-Go controller and menu model for the D28 tray
// control surface (docs/contracts.md §7.3). It contains no D-Bus binding, no
// process management, no routing, pool or state logic. A thin binder applies
// the tree this package builds, and every data change still goes through the
// existing management socket.
//
// # Boundaries
//
// Caller is the management boundary. It has the shape of control.Client.Call:
// the caller sends one control.Request and returns the decoded envelope. The
// package never invents a management operation; Refresh and Trigger use only
// the requests documented in docs/contracts.md §7.3. A transport failure or an
// error envelope (OK=false) is a failure, never a success with empty data.
//
// Lifecycle is the supervisor boundary. Quit and RestartEngine are the two
// process-level requests that cannot be management operations; Events carries
// the supervisor's restarting/ready notifications. A restart job succeeds only
// after restarting is observed before ready. A ready event that was not
// preceded by restarting is not evidence for this restart; the job reports
// "unconfirmed" at its deadline instead.
//
// Notifier receives only completed action/job outcomes. The controller never
// passes a destination, URL, credential or raw caller message to it.
//
// AutostartManager is the optional local XDG-autostart boundary. It matches
// *autostart.Manager, so internal/app can pass the manager directly.
//
// Terminal is the optional terminal-menu boundary. internal/app implements it
// with the fixed konsole/gnome-terminal/xterm allowlist and an explicit argv;
// the controller only reports success, a launch failure, or the exact manual
// command when no allowlisted terminal is installed.
//
// # Binder contract
//
// A Binder applies View by stable node keys, forwards clicks as
// Trigger(action, target) and feeds lifecycle events to HandleEvent. Runner is
// the implementation: it calls View on every poll tick (the restart timeout is
// evaluated there), RefreshAll at start, RefreshStatus
// every two seconds, RefreshLists every fifteen seconds, and RefreshCore
// immediately after an action and after a LifecycleReady event. The initial
// RefreshAll includes the expensive desktop/autostart reads; those two are not
// part of the periodic poll. The controller re-reads desktop status before a
// plain Quit, and an action that changes desktop/autostart
// state updates that state from its own response. TerminalMenu reaches the
// Terminal boundary; this package never starts a process itself. The runner
// must log only Action strings, never node labels or targets (D21).
//
// Backend is the rendering seam: Build creates the initial tree, Apply receives
// one deterministic batch of stable-key operations per changed View, and Close
// removes the item. A concrete StatusNotifierItem adapter is R15-D2; this
// package imports no D-Bus library.
package trayui
