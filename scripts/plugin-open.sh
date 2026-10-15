#!/bin/sh
# Action command of the herdr plugin: opens the popup pane named by $1.
# herdr actions run a command; popups are panes, so the action asks herdr
# (through its CLI, the plugin API) to open one.
set -eu

exec "${HERDR_BIN_PATH:-herdr}" plugin pane open --plugin "$HERDR_PLUGIN_ID" --entrypoint "$1"
