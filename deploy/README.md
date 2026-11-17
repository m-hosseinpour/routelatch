# Deploy: systemd user service

This directory contains the **systemd user** unit for `routelatch run`, the
foreground service command defined in `docs/contracts.md` §7.1. The process is
the base; the unit only adds start, stop and restart behavior. It does not
require root, TUN, a firewall change, or a system-wide proxy setting.

The unit deliberately keeps the user's home and network available. Hardening
such as `ProtectHome=`, `PrivateNetwork=` or a network namespace would hide the
XDG config/state directories or break direct egress, so it is not used. No proxy
environment variable is set in the unit: the launcher sets those for one child
process only.

## Install (documented only; do not run these during development)

```sh
install -Dm755 routelatch "$HOME/.local/bin/routelatch"
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

## Uninstall (documented only; do not run these during development)

Restore the optional desktop proxy **before** removing the binary. This restore
is offline: it needs the ownership snapshot in the RouteLatch state directory,
not a running service.

```sh
routelatch desktop proxy restore
systemctl --user disable --now routelatch.service
rm "$HOME/.config/systemd/user/routelatch.service"
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
