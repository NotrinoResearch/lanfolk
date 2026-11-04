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

2026-11-04, Windows 11 Pro 10.0.26300, Wintun driver 0.14, Lanfolk 0.1.0
(`lanprobe` from the installed build; the Lanfolk service was stopped during
the test):

| Test | Default metric (5) | Metric 1 (compatibility mode) |
| --- | --- | --- |
| (a) App socket (0.0.0.0) to 10.77.0.255 read from adapter | yes | yes |
| (a) Socket bound to 10.77.0.2 to 10.77.0.255 read from adapter | yes | yes |
| (b) App socket (0.0.0.0) to 255.255.255.255 read from adapter | **no** | yes |
| (b) Socket bound to 10.77.0.2 to 255.255.255.255 read from adapter | yes | yes |
| (c) Multicast 239.255.77.77 with IP_MULTICAST_IF=10.77.0.2 read from adapter | yes | yes |
| (c) Multicast 239.255.77.77 on the default interface read from adapter | yes | yes |
| (d) Broadcast written into adapter (dst 10.77.0.255), socket bound to 0.0.0.0 | yes | (not affected) |
| (d) Broadcast written into adapter (dst 10.77.0.255), socket bound to 10.77.0.2 | yes | (not affected) |
| (d) Broadcast written into adapter (dst 255.255.255.255), socket bound to 0.0.0.0 | yes | (not affected) |
| (d) Broadcast written into adapter (dst 255.255.255.255), socket bound to 10.77.0.2 | yes | (not affected) |
| (d) Socket bound to the broadcast address itself | n/a (bind not allowed by Windows) | (not affected) |
| (e) Multicast written into adapter (dst 239.255.77.77), socket joined on 10.77.0.2 | yes | (not affected) |

Observation: the only behavior that depends on the metric is a global
broadcast (255.255.255.255) sent from a socket bound to 0.0.0.0: Windows sends
it out of the lowest-metric interface, so it reaches Lanfolk only with metric
1. That is exactly what compatibility mode is for. Receiving broadcast and
multicast works with the normal metric.

### Elevated Windows network checks (TESTING.md section 4)

2026-11-04, same machine, Lanfolk 0.1.0 and 0.2.0, an active network:

| Check | Result |
| --- | --- |
| Adapter | `Lanfolk`, Up, 10.77.0.2/24, IP MTU 1380 |
| Metric | 9000 with automatic metric disabled; 1 with compatibility mode on; back to 9000 when it is turned off |
| Stopping the service | the adapter is removed |
| Routes | 10.77.0.0/24, its host and broadcast routes, 224.0.0.0/4, 255.255.255.255/32; no default route |
| Network profile | Private |
| Firewall | exactly 6 enabled rules in group `Lanfolk` with the expected names |
| `go test ./internal/winnet/` elevated | 49 passed, 1 skipped, 0 failed |

SMB/RDP reachability from a second machine: blocked (see "LAN behaviors
between two machines").

### Installer on a clean Windows 11 (TESTING.md section 7)

2026-11-04, `Lanfolk-0.2.0-setup.exe` in Windows Sandbox (Windows 11
Enterprise 26100, a fresh image each start), silent mode:

| Step | Result |
| --- | --- |
| Install | exit 0 in 12 s; service running and automatic; files in `C:\Program Files\Lanfolk`; 6 firewall rules; Apps entry "Lanfolk 0.2.0 / Lanfolk contributors"; Start Menu shortcut; PATH entry; `C:\ProgramData\Lanfolk` created; relay reachable |
| Join a network | invite code from A redeemed with `lanfolk-cli join`; joined in 10 s; Wintun adapter up with 10.77.0.3 |
| Uninstall while the app is open | the app was closed; service, adapter, firewall rules, program folder, PATH entry, Start Menu shortcut, Run value and Apps entry removed; nothing pending a restart; data folder kept |
| Install again | identity (friend code) unchanged |

Not covered: Windows 10, the interactive wizard (only its first page was
seen), "Yes" to deleting the data (the silent default is No), and a reboot
of the clean machine (Windows Sandbox cannot reboot).

### Installer (TESTING.md section 7, on the development machine)

2026-11-04, Windows 11 Pro 10.0.26300, `Lanfolk-0.2.0-setup.exe` (Inno Setup),
silent mode. This is not the clean-VM test of section 7; it covers upgrade,
uninstall and reinstall on a used machine.

| Step | Result |
| --- | --- |
| Upgrade over the 0.1.0 NSIS install | exit 0; service running and automatic; 6 firewall rules; old NSIS Apps entry removed, one Inno entry (0.2.0); PATH entry and Run value added; identity (friend code) unchanged |
| `lanprobe` from a new shell | found on PATH |
| Uninstall, data kept (silent default) | service, adapter, firewall rules, PATH entry, Run value, Start Menu folder and Apps entry removed; `%ProgramData%\Lanfolk` kept; no files left and no delete pending a restart. An empty `C:\Program Files\Lanfolk` stayed because the old NSIS installer had created it; fixed in the script (`RemoveDir`), not re-verified yet |
| Install again | identity unchanged |
| First start of the new app | the welcome screen asks for a name |

## LAN behaviors between two machines (Phase 7, `lanprobe test`)

2026-11-04, Lanfolk 0.2.0. A: Windows 11 Pro 26300 (the development machine,
10.77.0.2). B: a clean Windows 11 Enterprise 26100 in Windows Sandbox
(10.77.0.3), behind the Hyper-V NAT of A, so the two have separate NATs and
separate Wintun adapters. B joined A's network with an invite code in 10 s;
the path went direct (0.7 ms). `lanprobe serve` on B, `lanprobe test` on A.
Windows asked once on B to allow `lanprobe` through the firewall.

| Setup | TCP | UDP | Subnet bcast | Global bcast (bound) | Global bcast (0.0.0.0) | Mcast (IF set) | Mcast (default) | IP in payload | Private range | Blocked ports |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A to B, direct, compat off | PASS | PASS | PASS | PASS | FAIL (informational) | PASS | FAIL (informational) | PASS | PASS | PASS |
| A to B, direct, compat on | PASS | PASS | PASS | PASS | PASS | PASS | PASS | PASS | PASS | PASS |

Blocked ports: 135, 139, 445, 3389, 5985 and 5986 were all unreachable through
the tunnel. The two informational failures match the Wintun self-test: a
socket bound to 0.0.0.0 sends global broadcast and default-interface
multicast out of the lowest-metric adapter, which is Lanfolk only in
compatibility mode.

B to A and over the shared relay (B with Relay only, a second Sandbox
instance at 10.77.0.4; `lanprobe serve` on A):

| Setup | TCP | UDP | Subnet bcast | Global bcast (bound) | Global bcast (0.0.0.0) | Mcast (IF set) | Mcast (default) | IP in payload | Private range | Blocked ports |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| B to A, direct, compat off | PASS | PASS | PASS | PASS | PASS* (informational) | PASS | FAIL (informational) | PASS | PASS | PASS |
| B to A, shared relay, compat off | PASS | PASS | PASS | PASS | PASS* (informational) | PASS | FAIL (informational) | PASS | PASS | PASS |

\* Received with the source 172.27.94.240, B's address on the Hyper-V
switch: in this setup the two machines also share a virtual Ethernet, so a
0.0.0.0 broadcast reached A outside Lanfolk. Two homes do not share a LAN, so
this does not apply there.

Relayed path B to A (`lanprobe latency -peer 10.77.0.2 -count 2000`, both
ends on the same ISP line, relay in a remote data centre): 0 of 2000 lost,
p50 68.5 ms, p95 109.6 ms, p99 177.3 ms. The p50 equals the round trip to the
relay (about 70 ms from this line), as expected for host to relay to host.

Not covered yet: two physical machines on different ISPs.

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

Two Windows machines with real Wintun adapters (A and B as in the section
above, direct path over the Hyper-V NAT), `lanprobe latency -peer 10.77.0.3
-compare 172.27.93.75 -count 10000 -size 200 -rate 64`:

| Date | Setup | Raw p50 / p99 | Tunnel p50 / p99 | Overhead p50 / p99 | Lost |
| --- | --- | --- | --- | --- | --- |
| 2026-11-04 | A (Windows 11 26300) to B (Windows Sandbox, Windows 11 26100), direct path | 0.630 / 1.915 ms | 1.292 / 3.481 ms | **+0.662 / +1.567 ms** (target 1 / 3 ms: pass) | 0 of 10,000 (both) |

The raw path is between a host and a VM on one computer, so it is much
shorter than any internet path; the overhead is what Lanfolk adds on top of
the path (WireGuard, the Wintun driver on both sides, the bind and the LAN
layer).

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
