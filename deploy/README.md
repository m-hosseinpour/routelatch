# Deploy: systemd user service

> **Planned runtime model (D26, 2026-11-23):** the next release removes this
> packaged unit. `routelatch run` becomes the app/tray mode and `run --headless`
> the foreground/server mode; `autostart` and `service install` are explicit
> opt-ins. The unit flow below still describes the current package until R13
> lands. See `docs/requirements.md` D26 and `docs/implementation-plan.md` R13.

This directory contains the **systemd user** unit for `routelatch run`, the
foreground service command defined in `docs/contracts.md` §7.1. The process is
the base; the unit only adds start, stop and restart behavior. It does not
require root, TUN, a firewall change, or a system-wide proxy setting.

The unit deliberately keeps the user's home and network available. Hardening
such as `ProtectHome=`, `PrivateNetwork=` or a network namespace would hide the
XDG config/state directories or break direct egress, so it is not used. No proxy
environment variable is set in the unit: the launcher sets those for one child
process only.

## Install (a user action; this is the verified flow)

Installing or enabling a service needs the user's explicit request; the build
itself never does it.

```sh
install -Dm755 routelatch "$HOME/.local/bin/routelatch"
ln -sf routelatch "$HOME/.local/bin/rlch"
install -Dm644 deploy/systemd/routelatch.service \
    "$HOME/.config/systemd/user/routelatch.service"
systemctl --user daemon-reload
systemctl --user enable --now routelatch.service
```

Check it with:

```sh
systemd-analyze --user verify deploy/systemd/routelatch.service
systemctl --user status routelatch.service
journalctl --user -u routelatch.service
```

If the service should also start without an interactive login, the user can
enable lingering explicitly:

```sh
loginctl enable-linger "$USER"
```

### What was verified

This flow ran against a real `systemd --user` manager (R1) on Linux: the
`default.target.wants` symlink is created by `enable`, `start`/`stop`/`restart`
work, `stop` exits 0 after SIGTERM and closes both listeners, SIGKILL is
followed by the unit's on-failure restart with a byte-identical `state.json`, and
a foreign listener on the configured HTTP port makes startup exit 6 without a
fallback listener while `RestartPreventExitStatus` stops the restart loop.
Listeners were loopback-only and the management socket was `0600` in a `0700`
runtime directory. That R1 run did **not** perform a reboot or re-login; the
linger and `WantedBy=default.target` configuration was inspected, not observed
across a session restart. A later reboot on 2026-11-20 did observe the enabled
unit starting automatically at login, with mode, rules, pool and desktop-proxy
settings intact (see `docs/implementation-plan.md`); the gated test itself still
never reboots the machine.

The same supervision is reproducible with the gated acceptance check:

```sh
ROUTELATCH_SYSTEMD_TEST=1 go test -count=1 -v -run TestA13SystemdUserLifecycle ./internal/acceptance/
```

It writes a temporary unit into the caller's real user-manager unit directory,
runs it on an isolated data dir and random loopback ports, then removes it. Run
it only where installing a user service is authorized.

## If startup fails

`Type=simple` makes systemd report the start job as done when the process is
exec'd. A failure in the first milliseconds after that (for example an occupied
port) therefore leaves the unit `failed` even though `systemctl --user start`
returned success. Check the result with `systemctl --user status
routelatch.service` or `systemctl --user is-failed routelatch.service`.

| Exit | Meaning |
| --- | --- |
| 4 | another instance owns the data directory |
| 6 | a traffic listener could not bind (port collision); no traffic is redirected and no fallback port exists. The attempted addresses are in the service log (`listeners.bind_failed`, at the default log level) |
| 7 | unsupported configuration, such as a non-loopback listener |
| 10 | corrupt state or state written by a newer version |

Exits 4, 6 and 10 are in `RestartPreventExitStatus`, so systemd stops instead of
looping. Fix the cause (stop the foreign listener, free the port, repair the
state), then `systemctl --user reset-failed routelatch.service` and start again.
Exit 7 is not prevented; repeated unsupported configurations hit
`StartLimitBurst=5` within a minute and systemd stops trying as well.

## Uninstall (a user action)

Restore the optional desktop proxy **before** removing the binary. This restore
is offline: it needs the ownership snapshot in the RouteLatch state directory,
not a running service.

```sh
routelatch desktop proxy restore
systemctl --user disable --now routelatch.service
rm "$HOME/.config/systemd/user/routelatch.service"
rm -f "$HOME/.local/bin/rlch"
rm "$HOME/.local/bin/routelatch"
systemctl --user daemon-reload
```

The config and state directories are not removed by these commands; deleting
them would discard the user's rules and the desktop ownership record.

## Crash recovery

A crash can leave a GNOME/KDE desktop setting pointing at a dead listener. Do
not edit the setting by hand if it can be avoided:

```sh
routelatch desktop proxy restore
```

This command uses the recorded previous value and only reverts keys that still
hold the value RouteLatch wrote. Keys the user changed in the meantime are left
alone. If the tool (`gsettings`, `kwriteconfig6/5`) is not installed, the
command reports that and leaves the ownership record in place for a later retry.
