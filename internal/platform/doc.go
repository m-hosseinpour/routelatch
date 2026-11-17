// Package platform contains the Linux service and desktop integration glue for
// RouteLatch. It does not route traffic and it does not own state: callers pass
// the loopback listener addresses and, for desktop restore, the ownership
// snapshot stored in contract.DesktopProxyState.
//
// The package is deliberately small and testable without a desktop session:
//
//   - Launcher builds the environment for one child process. It never falls
//     back to running an application directly when the service is unhealthy,
//     and it replaces an inherited NO_PROXY with a list limited to the
//     local/LAN classes that RouteLatch always sends direct (D19, A11,
//     docs/contracts.md §7.4).
//   - Desktop applies and restores the optional desktop proxy on GNOME and KDE
//     Plasma only. Every external command goes through an injected
//     CommandRunner, and restore compares the current value with the value we
//     applied before changing anything (docs/contracts.md §4.8).
//
// Nothing here installs a service, enables a unit, or edits this machine's real
// desktop settings. Those actions belong to an explicit user command; the unit
// file and install instructions in deploy/ are static artifacts.
package platform
