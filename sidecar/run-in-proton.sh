#!/usr/bin/env bash
#
# Steam launch-option wrapper that starts lmu-bridge.exe under the SAME Proton
# as Le Mans Ultimate, so it shares the game's wineserver and can see the
# shared-memory telemetry object.
#
# The rF2 shared-memory plugin publishes a NAMED KERNEL OBJECT (not a file).
# Those objects live in wineserver memory, so the reader must run in the same
# wineserver as the game. Plain `wine lmu-bridge.exe` uses a different wine build
# (and container) than Proton, so it never sees the object. This wrapper avoids
# that by reusing the exact Proton entry point Steam hands us.
#
# Setup:
#   1) Put lmu-bridge.exe somewhere stable and point LMU_BRIDGE_EXE at it.
#   2) Steam -> Le Mans Ultimate -> Properties -> Launch Options:
#        /full/path/to/run-in-proton.sh %command%
#   3) Launch LMU normally from Steam.
#
# Tunables (env vars):
#   LMU_BRIDGE_EXE    path to lmu-bridge.exe   (default: ~/lmu-bridge.exe)
#   LMU_BRIDGE_ADDR   UDP destination          (default: 127.0.0.1:20440)
#   LMU_BRIDGE_DELAY  seconds to wait before starting the bridge, so the game
#                     and its plugin are up first (default: 40)
set -u

BRIDGE_EXE="${LMU_BRIDGE_EXE:-$HOME/lmu-bridge.exe}"
BRIDGE_ADDR="${LMU_BRIDGE_ADDR:-127.0.0.1:20440}"
DELAY="${LMU_BRIDGE_DELAY:-40}"
LOG="${LMU_BRIDGE_LOG:-/tmp/lmu-bridge.log}"
# Extra flags appended to the bridge, e.g. LMU_BRIDGE_EXTRA="-dump 480"
read -r -a EXTRA <<<"${LMU_BRIDGE_EXTRA:-}"

game_cmd=("$@")

# Locate the proton script within %command% (the token ending in "/proton").
proton=""
for a in "${game_cmd[@]}"; do
	case "$a" in
	*/proton) proton="$a" ;;
	esac
done

if [[ -n "$proton" && -x "$BRIDGE_EXE" ]]; then
	(
		sleep "$DELAY"
		echo "[run-in-proton] starting bridge: $proton run $BRIDGE_EXE -addr $BRIDGE_ADDR ${EXTRA[*]}" >>"$LOG" 2>&1
		# `run` (not waitforexitandrun) so it launches alongside the running game.
		"$proton" run "$BRIDGE_EXE" -addr "$BRIDGE_ADDR" -v "${EXTRA[@]}" >>"$LOG" 2>&1
	) &
else
	echo "[run-in-proton] NOT starting bridge (proton=[$proton] exe=[$BRIDGE_EXE])" >>"$LOG" 2>&1
fi

# Run the game in the foreground so Steam tracks its lifetime; when the game
# (and its wineserver) exits, the bridge process dies with it.
exec "${game_cmd[@]}"
