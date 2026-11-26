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
// # Binder contract
//
// A binder (R15-D1) applies View by stable node keys, forwards clicks as
// Trigger(action, target) and feeds lifecycle events to HandleEvent. It calls
// View on every poll tick (pending expiry and restart timeouts are evaluated
// there), RefreshAll at start, RefreshStatus every two seconds, RefreshLists
// every fifteen seconds, RefreshCore immediately after an action and after a
// LifecycleReady event, and RefreshDesktop/RefreshAutostart only at start,
// after an action and before a Stop/Restart/Quit confirmation. TerminalMenu is
// an action for the binder;
// this package does not start processes. The binder must log only Action
// strings, never node labels or targets (D21).
package trayui
