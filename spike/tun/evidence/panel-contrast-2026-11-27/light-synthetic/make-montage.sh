#!/usr/bin/env bash
# Regenerate the labelled 4x synthetic light-panel montage.
set -euo pipefail
cd "$(dirname "$0")"
montage -background '#e8e8e8' -fill '#202326' -pointsize 12 \
  -label '#eff0f1 real off' composite-eff0f1-real-off-missing.png \
  -label '#eff0f1 real error' composite-eff0f1-real-error-desired.png \
  -label '#eff0f1 fake off' composite-eff0f1-fake-off.png \
  -label '#eff0f1 fake error' composite-eff0f1-fake-error.png \
  -label '#eff0f1 fake on' composite-eff0f1-fake-on.png \
  -label '#eff0f1 fake starting' composite-eff0f1-fake-starting.png \
  -label '#eff0f1 fake conflict' composite-eff0f1-fake-conflict.png \
  -label '#fcfcfc real off' composite-fcfcfc-real-off-missing.png \
  -label '#fcfcfc real error' composite-fcfcfc-real-error-desired.png \
  -label '#fcfcfc fake off' composite-fcfcfc-fake-off.png \
  -label '#fcfcfc fake error' composite-fcfcfc-fake-error.png \
  -label '#fcfcfc fake on' composite-fcfcfc-fake-on.png \
  -label '#fcfcfc fake starting' composite-fcfcfc-fake-starting.png \
  -label '#fcfcfc fake conflict' composite-fcfcfc-fake-conflict.png \
  -tile 7x2 -geometry 152x148+6+6 light-panel-4x.png
