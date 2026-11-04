#!/usr/bin/env bash
# Lanfolk NAT simulation. Runs inside a privileged container (see run-local.sh).
#
# Topology (rebuilt for every scenario):
#
#   relay 203.0.113.10 --+
#   r1    203.0.113.11 --+-- br-wan (root namespace, 203.0.113.1/24)
#   r2    203.0.113.12 --+
#
#   r1 LAN 192.168.1.0/24: c1 .2, c3 .3      r2 LAN 192.168.2.0/24: c2 .2
#
# Every client runs lanfolk-service with a TUN device and is driven with
# lanfolk-cli. Results: $OUT_DIR/<scenario>/ and $OUT_DIR/summary.md.
set -euo pipefail

OUT_DIR=${OUT_DIR:-/out}
WORK=${WORK:-/tmp/natsim}
SCENARIOS=${SCENARIOS:-"easy-easy easy-hard hard-hard blocked-easy same-nat three-members relay-outage impairment"}
SETTLE_MAX=${SETTLE_MAX:-75}      # max seconds to wait for a stable path
PING_COUNT=${PING_COUNT:-20}
RELAY_IP=203.0.113.10
RELAY_ARG="$RELAY_IP:3478:443"
NET_NAME=natsim

declare -A PIDS=()   # name -> pid of background processes
NSES=()              # namespaces created
SCN=""               # current scenario
SDIR=""              # current scenario output directory

log() {
  local m
  m=$(printf '%s [%s] %s' "$(date +%H:%M:%S)" "${SCN:-natsim}" "$*")
  echo "$m" >&2
  if [ -n "$SDIR" ]; then echo "$m" >>"$SDIR/run.log"; fi
  return 0
}
die() {
  log "ERROR: $*"
  if [ -n "$SDIR" ]; then echo "$*" >"$SDIR/error.txt"; fi
  exit 1
}

ns() { local n=$1; shift; ip netns exec "$n" "$@"; }

# wait_until TIMEOUT_SECONDS DESCRIPTION COMMAND...
wait_until() {
  local timeout=$1 desc=$2 end
  shift 2
  end=$((SECONDS + timeout))
  while ! "$@" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$end" ]; then
      log "timeout after ${timeout}s waiting for: $desc"
      return 1
    fi
    sleep 0.5
  done
}

# ---------------------------------------------------------------- teardown

stop_proc() {
  local k=$1 pid=${PIDS[$1]:-}
  [ -n "$pid" ] || return 0
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.3
    done
    kill -KILL "$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
  unset "PIDS[$k]"
}

teardown() {
  local k n
  for k in "${!PIDS[@]}"; do stop_proc "$k"; done
  for n in "${NSES[@]:-}"; do
    [ -n "$n" ] && ip netns del "$n" 2>/dev/null || true
  done
  NSES=()
  ip link del br-wan 2>/dev/null || true
  # root-side veth ends vanish with their peers; remove leftovers anyway
  for n in vw-relay vw-r1 vw-r2 vw-r3; do ip link del "$n" 2>/dev/null || true; done
}

on_exit() {
  local rc=$?
  set +e
  teardown
  exit "$rc"
}
trap on_exit EXIT
trap 'exit 130' INT TERM

# ---------------------------------------------------------------- topology

# nft rules for a NAT router. $1 = profile: easy | hard | udp-blocked
nat_rules() {
  local prof=$1 masq="masquerade" blockudp=""
  case $prof in
    easy) ;;
    hard) masq="masquerade random" ;;
    udp-blocked) blockudp='iifname "lan0" oifname "wan0" meta l4proto udp counter drop' ;;
    *) die "unknown NAT profile $prof" ;;
  esac
  cat <<EOF
flush ruleset
table ip nat {
  chain postrouting {
    type nat hook postrouting priority 100; policy accept;
    oifname "wan0" $masq
  }
}
table ip filter {
  chain input {
    type filter hook input priority 0; policy accept;
    iifname "wan0" ip protocol icmp accept
    iifname "wan0" ct state established,related accept
    iifname "wan0" drop
  }
  chain forward {
    type filter hook forward priority 0; policy drop;
    iifname "lan0" oifname "lan0" accept
    $blockudp
    iifname "lan0" oifname "wan0" accept
    iifname "wan0" oifname "lan0" ct state established,related accept
  }
}
EOF
}

mk_ns() { ip netns add "$1"; NSES+=("$1"); ns "$1" ip link set lo up; }

# mk_router NAME WAN_HOST_OCTET LAN_NUMBER PROFILE
mk_router() {
  local r=$1 host=$2 lan=$3 prof=$4
  mk_ns "$r"
  ip link add "vw-$r" type veth peer name wan0 netns "$r"
  ip link set "vw-$r" master br-wan up
  ns "$r" ip addr add "203.0.113.$host/24" dev wan0
  ns "$r" ip link set wan0 up
  ns "$r" ip link add lan0 type bridge
  ns "$r" ip addr add "192.168.$lan.1/24" dev lan0
  ns "$r" ip link set lan0 up
  ns "$r" sysctl -qw net.ipv4.ip_forward=1
  nat_rules "$prof" | ns "$r" nft -f - || die "nft rules for $r ($prof) failed"
  ns "$r" nft list ruleset >"$SDIR/$r.nft"
}

# mk_client NAME ROUTER LAN_NUMBER HOST_OCTET
mk_client() {
  local c=$1 r=$2 lan=$3 host=$4
  mk_ns "$c"
  ip -n "$r" link add "v-$c" type veth peer name eth0 netns "$c"
  ip -n "$r" link set "v-$c" master lan0 up
  ns "$c" ip addr add "192.168.$lan.$host/24" dev eth0
  ns "$c" ip link set eth0 up
  ns "$c" ip route add default via "192.168.$lan.1"
}

build_wan() {
  ip link add br-wan type bridge
  ip addr add 203.0.113.1/24 dev br-wan
  ip link set br-wan up
  sysctl -qw net.bridge.bridge-nf-call-iptables=0 2>/dev/null || true
  mk_ns relay
  ip link add vw-relay type veth peer name eth0 netns relay
  ip link set vw-relay master br-wan up
  ns relay ip addr add "$RELAY_IP/24" dev eth0
  ns relay ip link set eth0 up
}

start_relay() {
  RELAY_UDP_PORT=3478 RELAY_TCP_PORT=443 RELAY_PUBLIC_HOST=$RELAY_IP \
    ns relay lanfolk-relay serve >>"$SDIR/relay.log" 2>&1 &
  PIDS[relay]=$!
  wait_until 15 "relay listening on TCP 443" ns relay bash -c 'exec 3<>/dev/tcp/127.0.0.1/443' \
    || die "relay did not start (see relay.log)"
  log "relay up (pid ${PIDS[relay]})"
}

stop_relay() { stop_proc relay; log "relay stopped"; }

sock() { echo "$WORK/$SCN/$1.sock"; }
cli() { local c=$1; shift; lanfolk-cli -json -pipe "$(sock "$c")" "$@"; }

start_client() {
  local c=$1
  mkdir -p "$WORK/$SCN/$c"
  ns "$c" lanfolk-service run -v -data-dir "$WORK/$SCN/$c" -pipe "$(sock "$c")" -relay "$RELAY_ARG" \
    >"$SDIR/$c.service.log" 2>&1 &
  PIDS[$c]=$!
  wait_until 20 "$c control socket" cli "$c" status || die "$c service did not start (see $c.service.log)"
}

# ---------------------------------------------------------------- queries

peer_path() { # CLIENT PEER_NICKNAME
  cli "$1" peers 2>/dev/null | jq -r --arg n "$2" '[.[]|select(.nickname==$n)|.path][0] // "none"' 2>/dev/null || echo none
}
peer_vip() {
  cli "$1" peers 2>/dev/null | jq -r --arg n "$2" '[.[]|select(.nickname==$n)|.virtual_ip][0] // empty'
}
nick_of() { case $1 in c1) echo alice ;; c2) echo bob ;; c3) echo carol ;; esac; }
relay_mode() { cli "$1" status 2>/dev/null | jq -r '[.relays[].mode][0] // "none"'; }
relay_up() { local m; m=$(relay_mode "$1"); [ "$m" != down ] && [ "$m" != none ]; }
both_relays_up() { relay_up "$1" && relay_up "$2"; }

friends_active() { # CLIENT NICK
  [ "$(cli "$1" friends | jq -r --arg n "$2" '[.[]|select(.nickname==$n)|.state][0] // ""')" = active ]
}
network_ready() { # CLIENT EXPECTED_MEMBERS
  cli "$1" networks | jq -e --arg n "$NET_NAME" --argjson m "$2" \
    '[.[]|select(.name==$n and .active==true)|.members|length]|.[0] == $m'
}

# ---------------------------------------------------------------- pings

PING_LOSS=""; PING_AVG=""
# ping_run CLIENT DST COUNT INTERVAL OUTFILE
ping_run() {
  local c=$1 dst=$2 count=$3 iv=$4 out=$5
  ns "$c" ping -c "$count" -i "$iv" -W 2 "$dst" >"$out" 2>&1 || true
  PING_LOSS=$(sed -n 's/.* \([0-9.]*\)% packet loss.*/\1/p' "$out" | tail -n1)
  PING_AVG=$(sed -n 's/^\(rtt\|round-trip\).* = [0-9.]*\/\([0-9.]*\)\/.*/\2/p' "$out" | tail -n1)
  : "${PING_LOSS:=100}"
  : "${PING_AVG:=n/a}"
}

# ---------------------------------------------------------------- scenario setup

# Per-scenario variables, set by define_scenario.
R1_PROF=easy; R2_PROF=easy; NODES="c1 c2"; A=c1; B=c2; EXP_AB=""; EXP_BA=""; KIND=basic; NOTE=""

define_scenario() {
  R1_PROF=easy; R2_PROF=easy; NODES="c1 c2"; A=c1; B=c2; EXP_AB=""; EXP_BA=""; KIND=basic; NOTE=""
  case $1 in
    easy-easy)     EXP_AB=direct; EXP_BA=direct ;;
    easy-hard)     R2_PROF=hard ;;
    hard-hard)     R1_PROF=hard; R2_PROF=hard; EXP_AB=shared-relay; EXP_BA=shared-relay ;;
    blocked-easy)  R1_PROF=udp-blocked; EXP_AB=tcp-fallback ;;
    same-nat)      NODES="c1 c3"; B=c3; EXP_AB=direct; EXP_BA=direct ;;
    three-members) R1_PROF=hard; R2_PROF=hard; NODES="c1 c2 c3" ;;
    relay-outage)  KIND=outage; EXP_AB=direct; EXP_BA=direct ;;
    impairment)    KIND=impair; EXP_AB=direct; EXP_BA=direct ;;
    *) echo "unknown scenario $1" >&2; return 1 ;;
  esac
}

router_of() { case $1 in c1|c3) echo r1 ;; c2) echo r2 ;; esac; }

setup_scenario() {
  build_wan
  mk_router r1 11 1 "$R1_PROF"
  mk_router r2 12 2 "$R2_PROF"
  start_relay
  local c
  for c in $NODES; do
    case $c in
      c1) mk_client c1 r1 1 2 ;;
      c3) mk_client c3 r1 1 3 ;;
      c2) mk_client c2 r2 2 2 ;;
    esac
  done
  # Sanity: every client reaches the relay's TCP port through its NAT (except
  # the UDP-blocked one, which uses TCP anyway) and the two routers see each other.
  for c in $NODES; do
    ns "$c" ping -c 2 -W 1 "$RELAY_IP" >/dev/null 2>&1 || die "$c cannot reach the relay through its router"
  done
  for c in $NODES; do start_client "$c"; done
  for c in $NODES; do
    wait_until 30 "$c relay mode up" relay_up "$c" \
      || log "warning: $c does not report the relay as reachable"
    log "$c relay mode: $(relay_mode "$c")"
  done
}

# Nicknames, friend codes both ways, network with all members.
join_everyone() {
  local c d code
  for c in $NODES; do cli "$c" nickname "$(nick_of "$c")" >/dev/null || die "nickname on $c"; done
  declare -A CODE=()
  for c in $NODES; do CODE[$c]=$(cli "$c" status | jq -r .friend_code); done
  for c in $NODES; do
    for d in $NODES; do
      [ "$c" = "$d" ] && continue
      cli "$c" friends add "${CODE[$d]}" "$(nick_of "$d")" >/dev/null || die "$c: friends add $d"
    done
  done
  for c in $NODES; do
    for d in $NODES; do
      [ "$c" = "$d" ] && continue
      wait_until 60 "$c sees $d as active friend" friends_active "$c" "$(nick_of "$d")" \
        || die "friendship $c <-> $d did not become active"
    done
  done
  log "friends active"
  cli "$A" networks create "$NET_NAME" >/dev/null || die "create network"
  local n=1
  for d in $NODES; do
    [ "$d" = "$A" ] && continue
    cli "$A" networks add "$NET_NAME" "$(nick_of "$d")" >/dev/null || die "add $d to network"
    n=$((n + 1))
  done
  NMEMBERS=$n
  for d in $NODES; do
    [ "$d" = "$A" ] && continue
    if ! wait_until 45 "$d has the network" network_ready "$d" "$NMEMBERS"; then
      # a member may hold the network inactive; activate and retry once
      cli "$d" networks activate "$NET_NAME" >/dev/null 2>&1 || true
      wait_until 15 "$d active network" network_ready "$d" "$NMEMBERS" || die "$d did not join the network"
    fi
  done
  log "all $NMEMBERS members joined"
}

# Wait for the A<->B paths to settle. Sets PATH_AB, PATH_BA, T_SETTLE.
settle_paths() {
  local start=$SECONDS end stable=0 last="" cur ab ba need
  end=$((SECONDS + SETTLE_MAX))
  PATH_AB=none; PATH_BA=none
  while [ "$SECONDS" -lt "$end" ]; do
    ab=$(peer_path "$A" "$(nick_of "$B")")
    ba=$(peer_path "$B" "$(nick_of "$A")")
    cur="$ab/$ba"
    if [ "$cur" = "$last" ] && [ "$ab" != none ] && [ "$ba" != none ]; then
      stable=$((stable + 1))
    else
      stable=0
    fi
    last=$cur
    PATH_AB=$ab; PATH_BA=$ba
    need=15
    if [ -n "$EXP_AB" ] && [ "$ab" = "$EXP_AB" ] && [ "$ba" = "${EXP_BA:-$ba}" ]; then need=4; fi
    [ "$stable" -ge "$need" ] && break
    sleep 1
  done
  T_SETTLE=$((SECONDS - start))
  log "paths settled after ${T_SETTLE}s: $A->$B $PATH_AB, $B->$A $PATH_BA"
}

snapshot() { # label
  local c
  for c in $NODES; do
    cli "$c" peers >"$SDIR/$c.peers.$1.json" 2>&1 || true
    cli "$c" status >"$SDIR/$c.status.$1.json" 2>&1 || true
  done
}

# ---------------------------------------------------------------- results

# record NAME PROFILES PATH_AB PATH_BA LOSS AVG RESULT NOTE
record() {
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$@" >"$SDIR/result.tsv"
}

# verdict: PASS/FAIL/INFO from expectations and ping loss
verdict() { # LOSS
  local v=PASS
  if [ -n "$EXP_AB" ] && [ "$PATH_AB" != "$EXP_AB" ]; then v=FAIL; fi
  if [ -n "$EXP_BA" ] && [ "$PATH_BA" != "$EXP_BA" ]; then v=FAIL; fi
  if ! awk -v l="$1" 'BEGIN{exit !(l <= 10)}'; then v=FAIL; fi
  if [ "$v" = PASS ] && [ -z "$EXP_AB$EXP_BA" ]; then v=INFO; fi
  echo "$v"
}

# ---------------------------------------------------------------- scenario bodies

scenario_body() {
  local vip
  setup_scenario
  join_everyone
  settle_paths
  snapshot up
  vip=$(peer_vip "$A" "$(nick_of "$B")")
  [ -n "$vip" ] || die "no virtual IP for $B on $A"
  log "ping target $B = $vip"
  # The first packets may be lost while the handshake completes.
  wait_until 30 "first ping $A -> $vip" ns "$A" ping -c 1 -W 1 "$vip" || log "warning: no ping reply before measurement"

  case $KIND in
    basic)
      ping_run "$A" "$vip" "$PING_COUNT" 0.2 "$SDIR/ping.txt"
      PATH_AB=$(peer_path "$A" "$(nick_of "$B")"); PATH_BA=$(peer_path "$B" "$(nick_of "$A")")
      ;;
    outage) outage_phase "$vip" ;;
    impair) impair_phase "$vip" ;;
  esac
  if [ "$SCN" = three-members ]; then
    local x3 y3
    x3=$(peer_path c1 carol); y3=$(peer_path c3 alice)
    NOTE="c1->c3 $x3, c3->c1 $y3; c2->c3 $(peer_path c2 carol), c3->c2 $(peer_path c3 bob)"
    ping_run c1 "$(peer_vip c1 carol)" 10 0.2 "$SDIR/ping-c1-c3.txt"
    NOTE="$NOTE; ping c1->c3 loss ${PING_LOSS}%"
    ping_run "$A" "$vip" "$PING_COUNT" 0.2 "$SDIR/ping.txt"
  fi
  if [ "$SCN" = same-nat ]; then
    NOTE="r1 has no hairpin rule, so a direct path proves the LAN addresses were used"
  fi
  snapshot end
  for c in $NODES; do cli "$c" diag "$SDIR/$c.diag.txt" >/dev/null 2>&1 || true; done
  record "$SCN" "$(profiles_text)" "$PATH_AB" "$PATH_BA" "$PING_LOSS" "$PING_AVG" "$(verdict "$PING_LOSS")" "$NOTE"
}

profiles_text() {
  local t="" c
  for c in $NODES; do
    local r; r=$(router_of "$c")
    local p=$R1_PROF; [ "$r" = r2 ] && p=$R2_PROF
    t="$t${t:+, }$c: $p"
  done
  echo "$t"
}

outage_phase() {
  local vip=$1 loss_during avg_during loss_after path_during_ab path_during_ba mode_back
  log "stopping the relay; the tunnel must survive on the direct path"
  stop_relay
  # 60 s of traffic without a relay
  ping_run "$A" "$vip" 150 0.4 "$SDIR/ping-outage.txt"
  loss_during=$PING_LOSS; avg_during=$PING_AVG
  path_during_ab=$(peer_path "$A" "$(nick_of "$B")"); path_during_ba=$(peer_path "$B" "$(nick_of "$A")")
  log "during outage: loss ${loss_during}%, paths $path_during_ab/$path_during_ba"
  cli "$A" status >"$SDIR/$A.status.outage.json" 2>&1 || true
  log "starting the relay again"
  start_relay
  if wait_until 90 "relay reachable again on $A and $B" both_relays_up "$A" "$B"; then
    mode_back="relay back"
  else
    mode_back="relay NOT seen again within 90 s"
  fi
  ping_run "$A" "$vip" "$PING_COUNT" 0.2 "$SDIR/ping-after.txt"
  loss_after=$PING_LOSS
  PATH_AB=$(peer_path "$A" "$(nick_of "$B")"); PATH_BA=$(peer_path "$B" "$(nick_of "$A")")
  NOTE="outage 60 s: loss ${loss_during}%, avg ${avg_during} ms, paths ${path_during_ab}/${path_during_ba}; after restart: ${mode_back}, loss ${loss_after}%, paths ${PATH_AB}/${PATH_BA}"
  # Verdict uses the outage ping as the main number; the paths must be direct
  # during the outage and the relay must come back.
  PING_LOSS=$loss_during; PING_AVG=$avg_during
  if [ "$path_during_ab" != direct ] || [ "$path_during_ba" != direct ] || [ "$mode_back" != "relay back" ] \
     || ! awk -v l="$loss_after" 'BEGIN{exit !(l <= 10)}'; then
    PATH_AB="$path_during_ab"; PATH_BA="$path_during_ba"
    EXP_AB=direct-and-recovered
  fi
}

impair_phase() {
  local vip=$1 loss_imp avg_imp ctl=ok loss_rec path_imp mode=netem
  log "netem on r1 wan0: delay 40ms 10ms loss 2% for 60 s"
  modprobe sch_netem >/dev/null 2>&1 || true
  if ns r1 tc qdisc add dev wan0 root netem delay 40ms 10ms loss 2% 2>"$SDIR/netem.err"; then
    ns r1 tc qdisc show dev wan0 >"$SDIR/netem.txt"
  else
    # Some kernels (for example the Docker Desktop WSL2 kernel) have no
    # sch_netem. Fall back to 2 % random loss on r1's WAN egress with nftables;
    # there is no delay or jitter in that mode, and the summary says so.
    mode="nft-loss-only"
    log "warning: tc netem unavailable ($(tr '\n' ' ' <"$SDIR/netem.err")); using nftables 2% random loss, no delay"
    ns r1 nft -f - <<'EOF' || die "nft loss fallback failed"
table ip impair {
  chain out {
    type filter hook postrouting priority 300; policy accept;
    oifname "wan0" numgen random mod 50 == 0 counter drop
  }
}
EOF
    ns r1 nft list table ip impair >"$SDIR/netem.txt"
  fi
  ping_run "$A" "$vip" 150 0.4 "$SDIR/ping-impaired.txt"
  loss_imp=$PING_LOSS; avg_imp=$PING_AVG
  cli "$A" peers >/dev/null 2>&1 || ctl=hung
  cli "$B" peers >/dev/null 2>&1 || ctl=hung
  path_imp="$(peer_path "$A" "$(nick_of "$B")")/$(peer_path "$B" "$(nick_of "$A")")"
  log "impaired: loss ${loss_imp}%, avg ${avg_imp} ms, control $ctl, paths $path_imp"
  if [ "$mode" = netem ]; then ns r1 tc qdisc del dev wan0 root || true; else ns r1 nft delete table ip impair || true; fi
  wait_until 60 "ping works after impairment removal" ns "$A" ping -c 1 -W 1 "$vip" || log "warning: no reply after impairment removal"
  sleep 5
  ping_run "$A" "$vip" "$PING_COUNT" 0.2 "$SDIR/ping-recovered.txt"
  loss_rec=$PING_LOSS
  PATH_AB=$(peer_path "$A" "$(nick_of "$B")"); PATH_BA=$(peer_path "$B" "$(nick_of "$A")")
  NOTE="impairment mode ${mode}$([ "$mode" = netem ] || echo ' (kernel has no sch_netem; 2% loss on r1 WAN egress only, no delay/jitter)'); impaired 60 s: loss ${loss_imp}%, avg ${avg_imp} ms, control ${ctl}, paths ${path_imp}; recovered: loss ${loss_rec}%, paths ${PATH_AB}/${PATH_BA}"
  # Main numbers = after recovery. The tunnel must not hang and must recover.
  if [ "$ctl" != ok ] || awk -v l="$loss_imp" 'BEGIN{exit !(l >= 90)}'; then
    PING_LOSS=100
  fi
}

run_one() {
  SCN=$1
  SDIR="$OUT_DIR/$SCN"
  rm -rf "$SDIR" "$WORK/$SCN"
  mkdir -p "$SDIR" "$WORK/$SCN"
  define_scenario "$SCN"
  log "=== scenario $SCN ($(profiles_text)) ==="
  local rc=0
  set +e
  ( set -euo pipefail; scenario_body )
  rc=$?
  set -e
  # keep the service logs written by the daemons themselves
  local c
  for c in c1 c2 c3; do
    if [ -d "$WORK/$SCN/$c/logs" ]; then cp -r "$WORK/$SCN/$c/logs" "$SDIR/$c.datalogs" 2>/dev/null || true; fi
  done
  for c in $NODES; do ns "$c" ip -4 addr >"$SDIR/$c.addr.txt" 2>/dev/null || true; done
  # parent-side teardown (the scenario ran in a subshell)
  teardown_scenario
  if [ ! -f "$SDIR/result.tsv" ]; then
    local why; why=$(cat "$SDIR/error.txt" 2>/dev/null || echo "scenario aborted (exit $rc), see run.log")
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$SCN" "$(profiles_text)" "-" "-" "-" "-" "FAIL" "harness/setup error: $why" >"$SDIR/result.tsv"
  fi
  return 0
}

# The scenario subshell started processes and namespaces that the parent does
# not know about, so tear down by name.
teardown_scenario() {
  local p n
  for p in $(pgrep -f 'lanfolk-(service|relay)' 2>/dev/null || true); do kill -TERM "$p" 2>/dev/null || true; done
  sleep 1
  for p in $(pgrep -f 'lanfolk-(service|relay)' 2>/dev/null || true); do kill -KILL "$p" 2>/dev/null || true; done
  for n in c1 c2 c3 r1 r2 relay; do ip netns del "$n" 2>/dev/null || true; done
  ip link del br-wan 2>/dev/null || true
  for n in vw-relay vw-r1 vw-r2; do ip link del "$n" 2>/dev/null || true; done
  PIDS=()
  NSES=()
}

# ---------------------------------------------------------------- summary

write_summary() {
  local f="$OUT_DIR/summary.md" s fail=0
  {
    echo "| Scenario | NAT profiles | Path A->B | Path B->A | Ping loss | Ping avg RTT | Result |"
    echo "| --- | --- | --- | --- | --- | --- | --- |"
    for s in $SCENARIOS; do
      [ -f "$OUT_DIR/$s/result.tsv" ] || continue
      IFS=$'\t' read -r name prof pab pba loss avg res note <"$OUT_DIR/$s/result.tsv"
      local pair="" ; define_scenario "$s"; pair="$A->$B"
      local lossc="$loss"; [ "$loss" != "-" ] && lossc="${loss}%"
      local avgc="$avg"; [ "$avg" != "n/a" ] && [ "$avg" != "-" ] && avgc="${avg} ms"
      echo "| $name ($pair) | $prof | $pab | $pba | $lossc | $avgc | $res |"
    done
    echo
    echo "Expectations: easy/easy direct; hard/hard shared-relay; udp-blocked tcp-fallback (seen by the blocked side); same NAT direct; relay-outage direct during the outage and the relay returns; impairment the tunnel answers and recovers. INFO means no expectation, only a working ping."
    echo
    echo "Notes:"
    for s in $SCENARIOS; do
      [ -f "$OUT_DIR/$s/result.tsv" ] || continue
      IFS=$'\t' read -r name prof pab pba loss avg res note <"$OUT_DIR/$s/result.tsv"
      [ -n "$note" ] && echo "- $name: $note"
    done
  } >"$f"
  for s in $SCENARIOS; do
    grep -q $'\tFAIL\t' "$OUT_DIR/$s/result.tsv" 2>/dev/null && fail=1
  done
  echo
  cat "$f"
  return $fail
}

# ---------------------------------------------------------------- main

main() {
  [ "$(id -u)" = 0 ] || { echo "run as root in a privileged container" >&2; exit 2; }
  mkdir -p "$OUT_DIR" "$WORK"
  [ -c /dev/net/tun ] || { mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 666 /dev/net/tun; }
  mkdir -p /var/run/netns
  teardown_scenario
  local s t0=$SECONDS
  for s in $SCENARIOS; do run_one "$s"; done
  log "all scenarios done in $((SECONDS - t0)) s"
  SCN=""
  write_summary
}

main "$@"
