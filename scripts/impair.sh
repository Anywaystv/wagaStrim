#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 wagaStrim contributors
# SPDX-License-Identifier: MIT
#
# Damages the media port on purpose so the buffer can be tested against the
# conditions it exists for. Needs root, because both platforms put traffic
# shaping in the kernel.
#
#   sudo scripts/impair.sh loss 2        two percent random loss
#   sudo scripts/impair.sh jitter 400    400 ms of jitter
#   sudo scripts/impair.sh cut 2         drop everything for two seconds
#   sudo scripts/impair.sh clear         remove all of it
#
# What each case is meant to show, from PLAN.md:
#
#   loss 2      NACK recovers it, nothing visible in OBS
#   jitter 400  the buffer absorbs it, output cadence stays steady
#   cut 2       output never stalls, which is the 2000 ms floor doing its job
#   cut 5       output stalls, then recovers to target with no permanent offset
set -euo pipefail

PORT="${WAGASTRIM_MEDIA_PORT:-7332}"
ACTION="${1:-}"
VALUE="${2:-}"

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "needs root: sudo $0 $*" >&2
    exit 1
  fi
}

usage() {
  sed -n '4,20p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

case "$(uname -s)" in
  Darwin) PLATFORM=darwin ;;
  Linux)  PLATFORM=linux ;;
  *) echo "no traffic shaping wired up for $(uname -s)" >&2; exit 1 ;;
esac

# macOS shapes with dummynet behind pf. The anchor keeps these rules separate
# from whatever else pf is doing, so clearing cannot take out unrelated rules.
darwin_apply() {
  dnctl pipe 1 config "$@"
  printf 'dummynet out proto udp from any to any port %s pipe 1\n' "$PORT" \
    | pfctl -a wagastrim -f - 2>/dev/null
  pfctl -E 2>/dev/null || true
}

darwin_clear() {
  pfctl -a wagastrim -F rules 2>/dev/null || true
  dnctl -q flush 2>/dev/null || true
}

# Linux shapes egress on the default route interface. netem on egress is enough
# because the impairment only has to exist in one direction to test recovery.
linux_iface() { ip route show default | awk '/default/ {print $5; exit}'; }

linux_apply() {
  local iface
  iface="$(linux_iface)"
  tc qdisc del dev "$iface" root 2>/dev/null || true
  tc qdisc add dev "$iface" root handle 1: prio
  tc qdisc add dev "$iface" parent 1:3 handle 30: netem "$@"
  tc filter add dev "$iface" protocol ip parent 1:0 prio 3 u32 \
    match ip protocol 17 0xff match ip dport "$PORT" 0xffff flowid 1:3
}

linux_clear() {
  local iface
  iface="$(linux_iface)"
  tc qdisc del dev "$iface" root 2>/dev/null || true
}

case "$ACTION" in
  loss)
    [ -n "$VALUE" ] || usage
    need_root "$@"
    if [ "$PLATFORM" = darwin ]; then darwin_apply plr "0.0${VALUE}"; else linux_apply loss "${VALUE}%"; fi
    echo "udp/$PORT losing ${VALUE}% of packets. Clear with: sudo $0 clear"
    ;;
  jitter)
    [ -n "$VALUE" ] || usage
    need_root "$@"
    if [ "$PLATFORM" = darwin ]; then
      # dummynet has no jitter primitive, so a delay plus a small queue produces
      # variable latency rather than a constant offset.
      darwin_apply delay "$VALUE" queue 4
    else
      linux_apply delay "${VALUE}ms" "${VALUE}ms" distribution normal
    fi
    echo "udp/$PORT delayed around ${VALUE}ms. Clear with: sudo $0 clear"
    ;;
  cut)
    [ -n "$VALUE" ] || usage
    need_root "$@"
    if [ "$PLATFORM" = darwin ]; then darwin_apply plr 1; else linux_apply loss 100%; fi
    echo "udp/$PORT fully cut for ${VALUE}s"
    sleep "$VALUE"
    if [ "$PLATFORM" = darwin ]; then darwin_clear; else linux_clear; fi
    echo "restored. Watch whether OBS ever stalled."
    ;;
  clear)
    need_root "$@"
    if [ "$PLATFORM" = darwin ]; then darwin_clear; else linux_clear; fi
    echo "impairment removed"
    ;;
  *) usage ;;
esac
