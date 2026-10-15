#!/usr/bin/env bash
# Run inside the first pane of the throwaway herdr (desk.tape): splits it
# into four panes, names them after the iOS demo world's agents and starts
# a scripted demo agent in each.
#
#   desk-setup.sh working|done
set -eu
mode=$1
here=$(cd "$(dirname "$0")" && pwd)
id() { sed -n 's/.*"pane_id":"\([^"]*\)".*/\1/p'; }
p1=$HERDR_PANE_ID
p2=$(herdr pane split "$p1" --direction right --no-focus | id)
p3=$(herdr pane split "$p1" --direction down --no-focus | id)
p4=$(herdr pane split "$p2" --direction down --no-focus | id)
set -- "$p1" auth "$p2" billing "$p3" migrate "$p4" docs
while [ $# -gt 0 ]; do
	herdr pane rename "$1" "$2" >/dev/null
	[ "$1" != "$p1" ] && herdr pane run "$1" "exec $here/demo-agent.sh $2 $mode" >/dev/null
	shift 2
done
exec "$here/demo-agent.sh" auth "$mode"
