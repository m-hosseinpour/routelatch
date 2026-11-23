// Package app implements the plain-`run` lifecycle from docs/contracts.md
// §7.5: a detached launcher, a supervisor that owns app.lock and app.owner, a
// foreground engine child (`run --headless`) and a pure-Go StatusNotifier tray
// child.
//
// The launcher pins the supervisor with a pidfd before following a readiness
// pipe. The supervisor publishes ready only after the engine reports
// status.ready and the tray confirms registration; every transition writes the
// marker before changing a child, and failure to write a transition enters the
// documented fail-closed window (release app.owner first, keep app.lock until
// the children are stopped). Teardown signals only through verified
// (pid, starttime) pidfds; the single raw SIGKILL is the launcher's own
// unreaped direct child when its pidfd could not be opened.
package app
