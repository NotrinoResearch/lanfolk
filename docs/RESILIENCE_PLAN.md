# Connection resilience plan (0.5.0)

Written 5 November 2026, after the Commandos 2 two-home tests (D-034, D-035).
This is the main work of release 0.5.0. Release 0.4.1 ships D-034 and D-035
first, so that the two-home test with osman can be repeated.

## Goal

Lanfolk promises that a network behaves like a LAN. A LAN never changes its
path, never loses its address and has no multi-second gaps. Lanfolk adds
events that a LAN does not have:

- path switches between direct and relay
- relay failover and TCP fallback
- NAT rebinding
- local network changes (Wi-Fi to cable, home Wi-Fi to a phone hotspot)
- tunnel restarts

Lanfolk must hide these events from games:

1. The virtual adapter and the virtual IP never go away while a network is
   active.
2. A path switch causes no gap, or a short gap that is measured and bounded.

How a game reacts to a gap (its timeouts, whether it freezes or drops a
player) is the game's design. Lanfolk does not add game-specific behavior
(COMPATIBILITY.md), and it does not retransmit lost packets inside the tunnel:
the games' own TCP does that, and retransmitting inside the tunnel as well
would make both layers wait for each other.

## What exists today

| Event | Mechanism | Reaction time |
| --- | --- | --- |
| Active path goes silent | Active path probed every 0.5 s, failed after 2 s, best standby path takes over. Standby paths are probed every 5 s. | about 2 s (not measured) |
| Failed path comes back | Retried after 30 s, backing off to 5 min | 30 s to 5 min |
| Local network change | Windows address and interface change callbacks: new candidates, probe bursts restart, candidates sent to members | not measured |
| UDP to the relay blocked | Relay client falls back to TCP 8443, retries UDP every 15 s | not measured |
| Relay down | Member relay, DHT to find members | not measured |
| Relay only switched | Both sides switch within seconds (D-035) | engine test |
| Compatibility mode switched | **The tunnel restarts**: the adapter and the virtual IP go away for a moment, every socket bound to it dies. Seen with Commandos 2: a running host was no longer listed. | outage |

TODO.md still lists two unchecked checks: "Change the network while
connected" and "Restart the relay server". None of the times above has
been measured on real lines.

## Practice we follow

These principles come from ICE and WebRTC (RFC 8445, consent freshness
RFC 7675), QUIC (path validation and migration, RFC 9000 sections 8 and 9),
NAT behavior (RFC 4787), and WireGuard and Tailscale. Lanfolk already
follows most of them:

- Identity separate from address. Members are known by key; the virtual IP
  stays fixed.
- Start on the relay, upgrade to direct, keep the relay as fallback.
- Keep standby paths warm. Use only validated paths, meaning paths that
  answered a probe. D-035 turned off WireGuard roaming because it used
  unvalidated addresses.
- Outbound keepalives well below NAT timeouts.
- React to local network changes at once.

Two practices are still open and are evaluated below (2.2, 2.3, 2.5):

- adaptive failure detection
- sending on two paths around a switch (make-before-break, redundancy)

## Measurable targets

These are starting values. Phase 1 measures the baseline; a target may change
with the data, and the change is recorded in DECISIONS.md.

| Scenario | Target |
| --- | --- |
| Compatibility mode on or off during a session | The adapter and virtual IP stay; a socket bound to the virtual IP keeps working; at most 1 s gap |
| Planned switch (relay only on or off, relay to direct upgrade) | No packet lost |
| Active direct path dies silently | Gap at most 2.5 s now, at most 1.5 s on lines under 100 ms RTT after 2.2 |
| NAT rebinding (our public port changes) | Back on a direct path within 3 s |
| Local network change (Wi-Fi to hotspot) | Traffic flows again within 5 s, no user action |
| UDP to the relay blocked while relayed | Traffic over TCP within 5 s |
| Relay restart | Clients registered again within 30 s, member traffic over direct or member relay meanwhile |
| Short blip of the direct path (3 s) | Back on direct within 10 s, not after the 30 s retry |

## Phase 1: measure (tools first)

### 1.1 Impairment hooks for engine tests

Extend the test-only socket filter in `internal/transport` into an impairment
hook. It works per remote address and per direction:

- loss (seeded random)
- delay and jitter
- blackhole windows ("drop everything to X from t1 to t2")
- rebinding (the peer starts to see us from a different port)

Add an engine test helper that sends a packet through the tunnel every 20 ms
and reports the longest gap and the loss. Each scenario in the target table
becomes an engine test with its target as the assertion. These tests run in
`go test`, need no Linux, no driver and no second machine, and follow the
pattern of `TestRelayOnlyOnOneSideStopsDirectTraffic`.

Tailscale's `tstest/natlab/vnet` is the model for the NAT behaviors (RFC 4787
mapping and filtering types). Lanfolk's own NAT simulation (MEASUREMENTS.md,
Phase 9) is extended rather than replaced.

### 1.2 Gap report in lanprobe

`lanprobe latency` gets a gap report: the longest gap, gaps over 250 ms with
their start times, and loss per second, as text and JSON. The same tool then
measures every two-machine scenario.

### 1.3 Two-machine scenario runner

A script (`scripts/resilience/`) runs each scenario between two Windows
machines while `lanprobe latency` runs:

- **Outages** with temporary Windows Firewall rules named `Lanfolk test *`:
  block the peer's public address (the direct path dies), block the relay's
  UDP port (TCP fallback), block everything for 3, 10 and 30 s.
- **Loss and delay** with clumsy (MIT, WinDivert). It installs a kernel
  driver, so it is for development machines only and is never part of the
  product.

The runner removes its rules at the end, also after a failure.

### 1.4 Baseline

Record the results of 1.1 and 1.3 in MEASUREMENTS.md, then confirm or adjust
the targets.

## Phase 2: fixes

Each fix comes with the test from phase 1 that shows it.

### 2.1 Compatibility mode without a tunnel restart

Switching compatibility mode changes the adapter metric and starts or stops
the broadcast capture. Neither needs a new adapter or a new WireGuard device.
Change both in place.

Test: a UDP socket bound to the virtual IP keeps sending and receiving across
both switches, and lanprobe sees at most 1 s gap.

If Windows needs an adapter restart for the metric in some case, the app
warns before the switch that running games will lose the connection.

### 2.2 Adaptive failure detection

Today the active path is failed after a fixed 2 s. Use the measured RTT and
jitter instead, for example `clamp(4 x SRTT + 4 x RTTVAR, 1 s, 2 s)`, with
the probe interval scaled to match.

The path manager simulation must show:

- fewer false failovers on jittery lines (the phone hotspot showed 55 ms
  jitter)
- faster failover on stable lines

Decide by data, record it in DECISIONS.md.

### 2.3 Make-before-break on planned switches

When the path manager leaves a path that still works (an upgrade to direct,
a better relay, Relay only switched on), send on both the old and the new
path for a short overlap, for example 1 s. WireGuard's replay window drops
the duplicates, so the receiver needs no change.

Test: no packet lost during a planned switch.

### 2.4 Faster return after a short blip

A path that worked shortly before it failed is retried sooner, for example
after 2, 4 and 8 s, before it falls back to the 30 s and 5 min backoff.
Paths that never worked keep the current backoff, so dead candidates cost
nothing extra.

Test: a 3 s blackhole of the direct path is back on direct within 10 s.

### 2.5 Redundant sending on bad lines (decision point)

On a line rated poor because of loss, small packets could go over two paths
at once, the way some gaming VPNs (Speedify, NoPing) do. This costs double
bandwidth on that peer and is only worth it if phase 1 shows real gains for
games.

Default: not built. Revisit with the data from 1.3.

### 2.6 Relay restart and TCP fallback

Run the two open TODO checks with the runner from 1.3 (relay restart,
blocked UDP). Fix what they find.

## Phase 3: field validation

1. Run the scenario runner between two homes (one machine on a phone
   hotspot), record the results in MEASUREMENTS.md.
2. Switch one machine from home Wi-Fi to the phone hotspot by hand during a
   game lobby and during `lanprobe latency`.
3. One real game sample per scenario class with Commandos 2. The automation
   exists in the Commandos 2 test rig. The game's own reaction is recorded,
   not judged.

## Done when

- Every row of the target table has an engine test or a measured two-machine
  result, recorded in MEASUREMENTS.md.
- 2.1, 2.3 and 2.4 are in; 2.2 and 2.5 are decided in DECISIONS.md.
- The TODO checks "Change the network while connected" and "Restart the
  relay server" are ticked.
