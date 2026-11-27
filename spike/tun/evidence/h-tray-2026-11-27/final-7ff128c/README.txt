R16-H tray final-run evidence for revision 7ff128c (2026-11-27)

This directory is a second, final-revision run of the harness in the parent
directory. It does not replace the original e4859db evidence in ../real and
../fake.

- HEAD: 7ff128ced6fc0f39a5301521a92bdd703f79d996 (clean tree at build time)
- Binary: <tmp>/r16h-final/bin/routelatch, CGO_ENABLED=0, built from that
  HEAD, unstamped (dev)
- Real run: <tmp>/r16h-final/real-20261127T100354Z (ports 48907/34959),
  10:03:54-10:03:58 UTC
- Fake run: <tmp>/r16h-final/fake-20261127T100441Z, 10:04:41-10:06:09 UTC
- Desktop: Plasma 6.6.7 / X11 :0, session bus of the authorized user

real/ is the real product engine + supervisor on an isolated data dir with no
TUN helper installed; the product supervisor started its own temporary tray
child. fake/ is the unchanged product `__app-tray` against the Python fake
management peer (visual-only states). Menu evidence is the dbusmenu GetLayout
tree; actions were sent as Event(id,"clicked"); no pointer click occurred.

Differences from the original evidence are tooling only: the run scripts now
take WORK/BIN/EV overrides, notification summaries are parsed by
../notify-summary.py, and requests-summary.txt is generated from requests.log.
Tray labels, icon colours and request counts reproduced the e4859db results
exactly; the GetLayout item ids differ per run and are not meaningful.

Scratch paths are shown as <tmp>; no machine-local path is retained.
