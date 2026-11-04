# Measurements

Field results that the plan's gates depend on. Rows never contain IP
addresses or the relay host. Add one row per run; keep older rows.

## NAT behavior per line (Phase 0, `natprobe`)

Run on each line: `natprobe -relay <relay-host> -relay-tcp-port 8443 -isp "<ISP name>"`
and paste the Markdown row it prints.

| ISP | Mapping | Port preservation | Relay RTT ms (min/med/p95) | TCP RTT ms (min/med/p95) | NAT timeout (keepalive) | IPv6 | DHT nodes | DHT put/get |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Dev home line (2026-11-04) | easy | no, sequential | 74.5/76.1/93.3 | 75.0/77.0/86.9 | 90-120 s (15 s) | no | 24 | ok/ok (3.1 s/4.1 s) |
| Türk Telekom | | | | | | | | |
| Superonline | | | | | | | | |
| TurkNet | | | | | | | | |
| Vodafone | | | | | | | | |
| Mobile | | | | | | | | |

Observations so far:

- The relay round trip from the development line is about 76 ms. Two players
  on similar lines see about 150 ms over the shared relay. That is above the
  "Poor" threshold (120 ms), so a relay closer to Turkey is worth adding
  (D-015).
- The NAT mapping survived 90 s of silence on the development line and
  expired at 120 s. The 15 s keepalive from the plan has a wide margin.

## Relay latency (Phase 2)

| Date | Client line | UDP RTT to relay (median) | Relay round trip between two sockets on the same line |
| --- | --- | --- | --- |
| 2026-11-04 | Dev home line | 76 ms | 147 ms |

## Wintun LAN behavior (Phase 0, `lanprobe wintun-selftest`)

Run in an elevated shell with `wintun.dll` next to `lanprobe.exe` and paste
the table it prints.

_Not run yet: needs administrator rights._

## LAN behaviors between two machines (Phase 7, `lanprobe test`)

_Not run yet: needs two machines on a Lanfolk network._

## Tunnel overhead (Phase 1, `lanprobe latency -compare`)

Target: at most 1 ms p50 and 3 ms p99 over the raw path.

Software overhead on one machine (`LANFOLK_OVERHEAD=1 go test -run TestTunnelOverhead ./internal/engine`):
UDP echo through two engines (WireGuard, custom bind, LAN layer, in-memory
adapters) against raw loopback UDP, 10,000 packets of 200 bytes. It excludes
the Wintun driver and the network, so the real-hardware run with
`lanprobe latency -compare` is still needed.

| Date | Setup | Raw p50 / p99 | Tunnel p50 / p99 | Overhead p50 / p99 | Lost |
| --- | --- | --- | --- | --- | --- |
| 2026-11-04 | Windows 11, i7-13700H, loopback, direct path | 0.095 / 0.312 ms | 0.195 / 0.919 ms | 0.100 / 0.606 ms | 0 of 10,000 |

_Real two-machine run: not run yet._

## Relay load (Phase 9)

`LANFOLK_LOADTEST=1 LANFOLK_LOADTEST_PAIRS=<n> go test -run TestRelayLoad ./internal/relay`:
n session pairs, both sides sending 64 packets/s of 200 bytes for 10 s,
through an in-process relay. Load generator and relay share the machine.

| Date | Host | Sessions | Offered | Delivered | Relay latency p50 / p99 |
| --- | --- | --- | --- | --- | --- |
| 2026-11-04 | Windows 11 dev laptop | 200 | 25,600 pps | 100% | 1.8 / 12.2 ms |
| 2026-11-04 | Windows 11 dev laptop | 1000 | 128,000 pps | 52.6% | 69 / 134 ms |
| 2026-11-04 | Linux container, 4 CPUs | 200 | 25,600 pps | 96.3% | 0.43 / 1.9 ms |
| 2026-11-04 | Linux container, 4 CPUs | 1000 | 128,000 pps | 96.5% | 0.19 / 1.8 ms |

Notes:

- With the original 4 MB socket buffers the overloaded Windows run queued
  packets for about 290 ms. The buffers are now 1 MB (D-021), so overload
  drops packets instead of delaying them.
- In the Linux runs delivery is about 96.4% at both loads, so the gap does
  not depend on load. It is most likely the harness (container CPU quota or
  packets at the start and end of the run), not relay capacity. This is
  open and should be checked on the deployment host.
- A realistic relay load is a few dozen sessions, far below these numbers.

## Protocol fuzzing (Phase 9)

`go test -run '^$' -fuzz <target> -fuzztime 20s <package>`, no failures:

| Date | Target | Executions |
| --- | --- | --- |
| 2026-11-04 | `internal/wire` FuzzParsers | 8.4 M |
| 2026-11-04 | `internal/peermsg` FuzzUnmarshal | 10.1 M |
| 2026-11-04 | `internal/network` FuzzUnmarshal | 7.0 M |
| 2026-11-04 | `internal/stun` FuzzParse | 8.1 M |
