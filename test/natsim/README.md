# NAT simulation

Tests the real Lanfolk binaries (`lanfolk-relay`, `lanfolk-service`, `lanfolk-cli`) against simulated home routers, without any real internet line. It covers the "NAT simulation", "CGNAT scenario", "Network impairment" and "Relay outage" rows of the test plan (Phase 9). CI is manual-only (D-002), so run it locally.

## What it builds

Everything runs in one privileged Linux container, with Linux network namespaces:

```
 relay 203.0.113.10 --+
 r1    203.0.113.11 --+-- br-wan (203.0.113.0/24)
 r2    203.0.113.12 --+

 r1 LAN 192.168.1.0/24:  c1 192.168.1.2, c3 192.168.1.3
 r2 LAN 192.168.2.0/24:  c2 192.168.2.2
```

- `relay` runs `lanfolk-relay` (UDP 3478, TCP 443).
- `r1` and `r2` are NAT routers (nftables). Profiles:
  - `easy`: `masquerade`. The source port is kept (endpoint-independent mapping); the connection tracker only lets replies from the address and port that was contacted (address-and-port-dependent filtering). Hole punching works.
  - `hard`: `masquerade random`. Every destination gets a different external port (endpoint-dependent mapping, like a symmetric NAT or a hard CGNAT). Hole punching cannot work.
  - `udp-blocked`: `easy` plus a firewall rule that drops all UDP leaving the LAN. Only TCP gets out, so the relay's TCP fallback is the only way.
  - Routers drop unsolicited packets from the WAN silently and have no hairpin rule.
- `c1`, `c2`, `c3` run `lanfolk-service run` with a real TUN device (`Lanfolk`, configured with `ip`) and are driven with `lanfolk-cli`.

For every scenario the script rebuilds the topology, sets nicknames, exchanges friend codes both ways, creates a network on `c1` (the owner), adds the other members, waits until they joined, waits for the paths to settle, pings the peer's virtual IP through the tunnel (`ping -c 20 -i 0.2`) and records the path each side reports (`lanfolk-cli -json peers`).

## Scenarios and expectations

| Scenario | Clients and NATs | Expectation |
| --- | --- | --- |
| `easy-easy` | c1 behind easy, c2 behind easy | both sides `direct` |
| `easy-hard` | c1 easy, c2 hard | recorded only (INFO) |
| `hard-hard` | c1 hard, c2 hard | both sides `shared-relay` |
| `blocked-easy` | c1 udp-blocked, c2 easy | c1 sees `tcp-fallback`; c2 is recorded |
| `same-nat` | c1 and c3 behind the same easy NAT | both sides `direct` (over the LAN addresses; the router has no hairpin, so a direct path proves it) |
| `three-members` | c1 and c3 behind one hard NAT, c2 behind another hard NAT | recorded only; the notes show all path pairs |
| `relay-outage` | easy/easy | stop the relay for 60 s with traffic running: still `direct`, no loss; start it again: the relay is reachable again and the tunnel is fine |
| `impairment` | easy/easy | 60 s of `tc netem delay 40ms 10ms loss 2%` on r1's WAN: the control socket keeps answering and pings keep working; after removal the tunnel recovers |

A row is `PASS` when the expectation holds and the ping loss is at most 10 %, `FAIL` when it does not (or the harness broke), and `INFO` when there is no expectation and the ping worked.

Netem needs the `sch_netem` kernel module. The Docker Desktop (WSL2) kernel has none, so there the impairment scenario falls back to 2 % random packet loss on r1's WAN egress with nftables (no delay, no jitter). The note in the summary says which mode ran. On a Linux host `run-local.sh` mounts `/lib/modules` so that the container can load `sch_netem` (on a CI runner install `linux-modules-extra-$(uname -r)` first).

## How to run

Needs Go and Docker with a Linux engine (privileged containers allowed).

```bash
test/natsim/run-local.sh                              # all scenarios, about 5 minutes with a warm image cache
SCENARIOS="easy-easy hard-hard" test/natsim/run-local.sh   # a subset
```

In Git Bash the script already sets `MSYS_NO_PATHCONV=1` for its Docker calls. The script builds the Linux binaries into `test/natsim/bin/`, builds the image `lanfolk-natsim:local`, runs the container (`--privileged --device /dev/net/tun`), copies `/out` to `test/natsim/out/` and removes the container.

Environment variables: `SCENARIOS` (space-separated list), `SETTLE_MAX` (seconds to wait for stable paths, default 75), `PING_COUNT` (default 20). The exit code is 1 if any row is `FAIL`.

`test/natsim/run.sh` is the test driver inside the container; it needs root and a privileged container, so do not run it on your host directly.

## Reading the results

- `test/natsim/out/summary.md`: the table (scenario, NAT profiles, path A to B, path B to A, ping loss, ping average RTT, result) and one note per scenario. "A to B" is the first client in the scenario column (`c1->c2`): the path that A reports for B, and the reverse.
- `test/natsim/out/<scenario>/` has the logs of that scenario:
  - `run.log`: the script's progress with timestamps; `error.txt` if the harness failed.
  - `relay.log`, `c1.service.log` (verbose), `c1.datalogs/` (the service's own log files).
  - `r1.nft`, `r2.nft`: the NAT rules in effect.
  - `*.peers.up.json`, `*.peers.end.json`, `*.status.*.json`, `*.diag.txt`: what the clients reported.
  - `ping*.txt`: raw ping output.

Paths: `direct` (hole punch worked or same LAN), `member-relay` (another member relays), `shared-relay` (the relay forwards UDP), `tcp-fallback` (the relay forwards over TCP), `none` (no path yet).

A failure is either a harness problem (the row says "harness/setup error" and `error.txt` explains) or a real Lanfolk problem (the paths differ from the expectation). In the second case look at the two `*.service.log` files and `*.diag.txt` of the scenario.

The simulated NATs are an approximation of real routers: Linux conntrack is a good model of an easy NAT and of a symmetric one, but not of every CGNAT (port-block allocation, hairpinning, long idle timeouts). Results complement, and do not replace, `natprobe` measurements on real lines.

Do not use real addresses here. Everything uses documentation and private ranges.
