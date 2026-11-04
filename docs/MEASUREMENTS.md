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

_Not run yet._
