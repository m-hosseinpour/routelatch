# Deploy: app/tray, autostart and opt-in user service

This page describes the runtime model from R13 onward (`docs/contracts.md`
§7.5, requirements D26). The package **ships no systemd unit, autostart entry
or maintainer script**. These explicit choices replace the old packaged unit:

| You want | Command | Result |
| --- | --- | --- |
| Desktop app with a tray icon | `routelatch run` | Detached app/tray; the command returns after readiness and survives closing the terminal |
| Foreground engine for a terminal, SSH, container or script | `routelatch run --headless` | Blocks in the foreground without a tray; Ctrl-C stops it cleanly |
| Start at login on a desktop | `routelatch autostart enable` | Writes an XDG autostart entry for the default instance |
| Server/VPS supervision by systemd | `routelatch service install` | Writes and enables an opt-in **systemd user unit** in the manager's config base |

`routelatch autostart` and `routelatch service install` are mutually exclusive
for one user; each setup command refuses while the other managed setup exists,
and `status` reports the conflict. Choose one. Only one engine may own a data
directory (the hard invariant); different `--data-dir` values are independent
instances and can collide on ports.

## Requirements and boundary

Plain `routelatch run` (app mode) needs a permitted `pidfd_open`: **Linux ≥5.3**
with pidfd not blocked by seccomp/EPERM (R-21). If pidfd is unavailable, `run`
exits `7` with reason `pidfd_unavailable` and starts no process. App mode also
needs a StatusNotifier tray host; GNOME without an extension and SSH/headless
sessions do not provide one, so they exit `7` with reason `tray_unavailable`.

`run --headless` does not need pidfd or a tray. It is the supported alternative
in those environments, and it is the only mode a service or unit may run. Plain
`run` deliberately detaches and is not suitable inside a systemd unit: systemd
would see its main process exit immediately.

No current mode needs root, TUN, a firewall change or a system-wide proxy
setting. Listeners remain loopback-only under the documented policy. The planned
post-v1 TUN coverage layer (D29/R16) is opt-in, uses a separate privileged
helper installed by `sudo routelatch tun setup`, and is not part of v1.1.0.

## Desktop app and foreground mode

```sh
# detached app + tray; returns once status.ready is true and the tray registered
routelatch run

# foreground engine, no tray; suitable for SSH and services
routelatch run --headless
```

The app's log path is printed by `routelatch run` and is also in `status`/the
`--json` envelope. Quit from the tray icon stops the supervisor and its engine
child. The tray menu also exposes status, mode changes (including Stop listening
and Restart engine), proposals, approved rules, pool, desktop-proxy status,
diagnostics, backup, the autostart toggle and the Terminal menu; destructive
actions require a second confirmation. See "Tray control surface" in
`README.md` for the action map and limits. In foreground mode Ctrl-C (SIGINT) or
SIGTERM stops the engine.

## Login autostart (desktop only)

```sh
routelatch autostart enable
routelatch autostart status
routelatch autostart disable
```

`enable` writes `$XDG_CONFIG_HOME/autostart/routelatch.desktop` (fallback
`~/.config/autostart/routelatch.desktop`) with `Exec="<absolute>" run`,
`Terminal=false`, `NoDisplay=true` and an ownership marker. It is idempotent,
does not start the app now, does not stop a running app, and refuses to
overwrite a foreign file (exit `6`). It probes the tray host and pidfd before
writing or claiming the entry is already enabled (exit `7` with
`tray_unavailable` or `pidfd_unavailable`).

Autostart manages the **default instance only**. All three verbs reject
`--data-dir` with exit `2`; for a custom data directory use
`routelatch service install --data-dir <dir>` instead.

## Opt-in systemd user service

```sh
routelatch service install
routelatch service status
routelatch service uninstall
```

`install` resolves the unit path from the user manager's own configuration base
(`systemctl --user show-environment`, falling back to `~/.config` only when the
manager confirms that base), writes `<manager-config-base>/systemd/user/routelatch.service`,
runs `systemctl --user daemon-reload` and `enable --now`, and reports success
only after the same data directory answers `status.ready=true` with its required
listeners open. The unit is data only: `Type=simple`,
`ExecStart=<absolute> [--data-dir <abs>] run --headless`,
`Restart=on-failure`, `RestartSec=2s`,
`RestartPreventExitStatus=1 4 6 7 9 10`, `KillSignal=SIGTERM`,
`KillMode=mixed`, `TimeoutStopSec=20s`, `WantedBy=default.target`. It has no
`User=`/`Group=`, no proxy environment and no hardening that would hide the
user's config/state or direct egress.

- `install` is idempotent. It does not rewrite a matching unit; it refuses a
  differing unit without `--force` (exit `6`).
- `install --force` updates an owned unit, or adopts a regular, user-owned,
  **inactive** markerless unit (for example a hand-written one) and enables the
  service. An active markerless unit is treated as another running engine and
  refused (exit `6`).
- `--data-dir <abs>` makes the unit serve that absolute instance and omits the
  XDG `Environment=` pinning. Without `--data-dir`, the manager config/state
  bases are pinned so shell environment differences cannot move the socket.
- A failure after writing rolls the unit back to its previous content and
  enabled/active state. A readiness timeout exits `8`; documented child exits
  map to `1`, `4→6`, `6`, `7`, `9` and `10`.
- When lingering is off, `install` prints only
  `sudo loginctl enable-linger <user>`. It never runs sudo and refuses to run
  for another user through `SUDO_USER`/`PKEXEC_UID` (exit `9`).
- `uninstall` checks ownership first and only stops, disables and removes a unit
  carrying the RouteLatch marker. It leaves config, state and linger untouched.

Check it with:

```sh
systemctl --user status routelatch.service
journalctl --user -u routelatch.service
routelatch service status
```

### If startup fails

`Type=simple` makes systemd report the start job as done when the process is
exec'd, so the install command also waits for real readiness. Check a failed
unit with `systemctl --user status routelatch.service` or
`systemctl --user is-failed routelatch.service`.

| Exit | Meaning |
| --- | --- |
| 1 | internal error; terminal, not retried |
| 4 | another instance (including an old R12 engine) owns the data directory |
| 6 | a traffic listener could not bind (port collision) or a lock/integrity conflict; no traffic is redirected and no fallback port exists |
| 7 | unsupported configuration, such as a non-loopback listener or no systemd user manager |
| 8 | service readiness timed out; install rolled back |
| 9 | the command was run for another user through sudo/pkexec |
| 10 | corrupt state or state written by a newer version |

Exits `1`, `4`, `6`, `7`, `9` and `10` are in `RestartPreventExitStatus`, so a
systemd restart does not loop on them. Fix the cause (stop the foreign listener,
free the port, repair or restore the state), then
`systemctl --user reset-failed routelatch.service` and start again.

## Uninstall

Restore the optional desktop proxy **before** removing the binary. This restore
is offline: it needs the ownership snapshot in the RouteLatch state directory,
not a running service.

```sh
routelatch desktop proxy restore
routelatch service uninstall    # only if you installed the opt-in service
routelatch autostart disable    # only if you enabled login autostart
rm -f "$HOME/.local/bin/rlch"
rm "$HOME/.local/bin/routelatch"
```

For a package install, remove it with the package manager after disabling the
service/autostart. The config and state directories are not removed by these
commands; deleting them would discard the user's rules and the desktop
ownership record.

## Migrating from the old packaged unit

Releases up to `v0.1.3` shipped a systemd user unit whose
`ExecStart` was `routelatch run` (the old foreground command). In this revision
plain `run` is the detached app/tray, so **stop and remove the old unit before
upgrading**. Do not leave it enabled: it would either fail to keep a service
process or start an app that a service should own.

1. Stop and disable the old unit:
   ```sh
   systemctl --user disable --now routelatch.service
   ```
2. Remove the old unit files and their enablement symlink. The manager's config
   base may differ from your shell's `$XDG_CONFIG_HOME`; check it with
   `systemctl --user show-environment | grep XDG_CONFIG_HOME`.
   ```sh
   # old package unit (root-owned); a package upgrade removes it too
   sudo rm -f /usr/lib/systemd/user/routelatch.service
   # old tarball/manual unit and its wants symlink
   rm -f "$HOME/.config/systemd/user/routelatch.service"
   rm -f "$HOME/.config/systemd/user/default.target.wants/routelatch.service"
   # if the manager base differs, remove that copy and symlink as well
   systemctl --user daemon-reload
   ```
3. Upgrade the package or replace the tarball binary, keeping `rlch` as the
   same relative symlink (the short command is unchanged).
4. Choose one setup:
   ```sh
   routelatch autostart enable    # desktop login
   # or
   routelatch service install     # server/VPS, opt-in user unit
   ```

If the old unit is forgotten, the migration window makes the failure explicit
rather than starting a second engine:

- **Same data directory:** the old engine holds the legacy lock for that
  instance (its `<StateDir>/state.lock` and the R12 runtime layouts). A new
  engine probes and refuses that lock with exit `4` before binding anything,
  **even when the old engine is in `stopped` mode with no listener**.
- **Different data directory (for example old default engine + new
  `--data-dir`):** the two instances are independent by design and may collide
  on ports. Migration must stop and disable the old instance explicitly; the
  new locks are deliberately not global.
- **Custom `XDG_RUNTIME_DIR`:** an old default engine started with an
  unpredictable custom runtime may be invisible to the migration probe. Stop
  the old unit explicitly; that step is always required, not optional.

This legacy-lock accommodation exists for one release only. After it is
removed, an old engine must have been stopped; the explicit `disable --now`
above is the supported migration.

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

## What was verified

The package and unit layout are checked offline: `scripts/check-package.sh`
rejects any unit, autostart entry, systemd path or maintainer script in the
tarball, `.deb` and RPM spec. The `service`/`autostart` behavior, unit content,
ownership, idempotence, adoption with `--force`, rollback and conflict handling
are covered by fake-manager unit tests that never touch a real systemd manager
(`go test ./cmd/routelatch ./internal/service`).

A read-only check against the real user manager is opt-in; it only reads status
and does not write, start, stop or reload anything:

```sh
ROUTELATCH_SYSTEMD_SERVICE_STATUS=1 go test -count=1 -v -run TestRealUserManagerServiceStatus ./cmd/routelatch/
```

A real `service install`/uninstall/rollback run, real linger, and real
autostart at login remain unverified on this host (its live unit must not be
touched) and are tracked for R13-F/release acceptance. The old packaged-unit
lifecycle test was retired together with the packaged unit.
