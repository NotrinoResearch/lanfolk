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

## NAT simulation (Phase 9)

Date: 2026-11-04. Run with `test/natsim/run-local.sh` (Linux network
namespaces and nftables inside a privileged Docker container on Windows 11 /
Docker Desktop, WSL2 kernel 5.15). Two full runs gave identical paths and
results; one full run takes about 5 minutes with a warm image cache. See
`test/natsim/README.md` for the topology and the NAT profiles (`easy` =
`masquerade`, `hard` = `masquerade random`, `udp-blocked` = easy plus all
outbound UDP dropped).

| Scenario | NAT profiles | Path A->B | Path B->A | Ping loss | Ping avg RTT | Result |
| --- | --- | --- | --- | --- | --- | --- |
| easy-easy (c1->c2) | c1: easy, c2: easy | direct | direct | 0% | 0.913 ms | PASS |
| easy-hard (c1->c2) | c1: easy, c2: hard | shared-relay | shared-relay | 0% | 0.846 ms | INFO |
| hard-hard (c1->c2) | c1: hard, c2: hard | shared-relay | shared-relay | 0% | 1.499 ms | PASS |
| blocked-easy (c1->c2) | c1: udp-blocked, c2: easy | tcp-fallback | shared-relay | 0% | 1.159 ms | PASS |
| same-nat (c1->c3) | c1: easy, c3: easy | direct | direct | 0% | 0.962 ms | PASS |
| three-members (c1->c2) | c1: hard, c2: hard, c3: hard | shared-relay | shared-relay | 0% | 1.289 ms | INFO |
| relay-outage (c1->c2) | c1: easy, c2: easy | direct | direct | 0% | 0.879 ms | PASS |
| impairment (c1->c2) | c1: easy, c2: easy | direct | direct | 0% | 0.513 ms | PASS |

Notes:

- same-nat: the router has no hairpin rule, so a direct path proves that the
  LAN addresses were used.
- three-members: c1 and c3 share one hard NAT, c2 is behind another. c1<->c3
  direct (LAN addresses), c2<->c3 and c1<->c2 shared-relay; ping c1->c3 loss 0%.
- relay-outage: the relay was stopped for 60 s with a ping running (150
  packets, 0% loss, both sides stayed direct); after the restart the relay
  was reachable again on both clients, 0% loss, still direct.
- impairment: this Docker Desktop kernel has no `sch_netem`, so the run used
  the fallback: 2% random loss on r1's WAN egress with nftables, no delay or
  jitter (`tc netem delay 40ms 10ms loss 2%` is used where the kernel has it).
  During the 60 s of impairment the ping loss was 3.3% (150 packets), the
  control socket kept answering and both paths stayed direct; after the
  removal the loss was 0%. The delay and reordering part of the plan's
  impairment test has not been exercised yet.
- RTTs are below 2 ms because everything runs on one host; they show the
  tunnel and relay overhead only.
