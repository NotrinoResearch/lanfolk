# Decisions and blockers

This log records decisions made during implementation, especially where the
work differs from [the implementation plan](lanfolk-open-source-virtual-LAN-implementation-plan.md).
Each entry says what changed, why, and what it affects. Newest entries are at
the bottom.

## D-001 Phase gates that need real hardware stay open

**Plan:** Execute phases in order; do not start the next phase until the gate
passes.

**Decision:** Several gates need real ISP lines, two Windows machines or
administrator rights (`[Human]` tasks, Wintun tests, CGNAT measurements). The
agent cannot pass them alone. Implementation continues phase by phase with
automated tests as the technical gate. Human gates remain unticked in the
plan, and their tools and instructions are prepared so the results can be
collected later in `docs/MEASUREMENTS.md`.

**Impact:** Features exist before their field measurements. Thresholds that
the plan says must come from measurements (keepalive interval, quality
thresholds, relay latency) use the plan's initial values and are configurable.

## D-002 CI is disabled for now; builds and tests run locally

**Plan:** Set up CI; build and scan the relay image in CI; NAT simulation in CI.

**Decision:** At the user's request, the GitHub Actions workflows only run on
manual `workflow_dispatch`. All verification runs locally:

- Go 1.27.3 and MinGW-w64 GCC 16.2 (for cgo and `go test -race`) on Windows.
- Docker Desktop for Linux builds, image scans and NAT simulation.

**Impact:** Re-enable the `push` and `pull_request` triggers in
`.github/workflows/*.yml` when CI is wanted again.

## D-003 Relay TCP fallback runs on 8443 on the current VPS

**Plan:** `RELAY_TCP_PORT=443`, or another free port if 443 is occupied.

**Decision:** On the deployment VPS, TCP 443 belongs to an existing reverse
proxy. The relay uses TCP 8443. UDP 3478 is unchanged. The defaults in the
code and in `deploy/relay/.env.example` stay at 443 for other self-hosters.

## D-004 Two firewall rules were added on the VPS with explicit approval

**Plan:** Do not change firewall rules. If a firewall blocks traffic, stop and
ask the user.

**Decision:** UFW was active with default deny, so the relay ports were
blocked. Deployment stopped and the user was asked. The user explicitly
approved adding exactly two rules: `ufw allow 3478/udp` and
`ufw allow 8443/tcp`, both with the comment `lanfolk-relay`. No other rule or
service was changed. Rollback: `ufw delete allow 3478/udp` and
`ufw delete allow 8443/tcp`.

## D-005 The relay image is built locally and copied to the VPS

**Plan:** Create only `lanfolk-relay` resources on the VPS.

**Decision:** Building on the VPS would pull the Go builder image there.
Instead the image is built locally, exported with `docker save`, uploaded to
`/opt/lanfolk-relay`, loaded with `docker load`, and the tarball is deleted.
The final image is `FROM scratch` with one static binary (about 7 MB), so
the VPS gains exactly one image.

## D-006 Non-root relay binds low ports through a file capability

**Plan:** The container runs as a non-root user with host networking.

**Decision:** With host networking, Docker's per-container
`ip_unprivileged_port_start=0` sysctl does not apply. The binary therefore
carries `cap_net_bind_service` as a file capability, set in the build stage.
The compose file drops all capabilities and adds only `NET_BIND_SERVICE`.
`no-new-privileges` is not set because it would prevent the file capability
from taking effect when the relay binds port 443.

## D-007 Relay data header is 17 bytes, member relay header is 25 bytes

**Plan:** "Relay paths add 16 more bytes"; MTU budget 1492 - 40 - 8 - 32 - 16.

**Decision:** Shared relay data frames carry a 1-byte frame kind and the
16-byte session ID (17 bytes) so that one UDP socket can demultiplex STUN,
WireGuard, relay, control and member relay frames by the first byte. Member
relay frames add an 8-byte key ID of the final peer (25 bytes). The 1380-byte
virtual MTU still fits: 1380 + 32 + 17 + 48 = 1477 and 1380 + 32 + 25 + 48 =
1485, both at most 1492. A unit test enforces this budget.

## D-008 Friend codes carry a 4-byte checksum

**Plan:** A friend code is the z-base32 text form of the 32-byte public key
with the `lanfolk1` prefix.

**Decision:** The encoded body is the key followed by the first 4 bytes of
SHA-256("lanfolk1" || key). A code that was mistyped or cut while pasting is
rejected locally instead of creating a friend entry for a key nobody holds.
Codes are 66 characters long. Spaces, dashes and letter case are ignored.

## D-009 WireGuard keys are derived from the identity key

**Plan:** Identity is an Ed25519 key; WireGuard and X25519 pair secrets are
used.

**Decision:** Each user has one key. The X25519 private scalar is derived
from the Ed25519 seed (SHA-512 and clamping, as in libsodium), and a peer's
X25519 public key is the Montgomery form of its Ed25519 public key. The same
X25519 values are used for the WireGuard static keys and for the pair secret
(HKDF-SHA256 with domain-separated labels). The friend code alone is enough
to configure a WireGuard peer.

## D-010 DHT records are signed with a per-pair key

**Plan:** BEP 44 mutable items with salt and encryption keys derived from the
pair secret.

**Decision:** The BEP 44 signing key is also derived from the pair secret and
the publisher's key, instead of using the identity key. Records stay
unlinkable to the user's public identity. Only the two peers can compute the
target, read the record or publish it.

## D-011 High-resolution clock for latency measurements

**Plan:** Not covered.

**Decision:** Go's `time.Now` advances in about 0.5 ms steps on Windows
(measured: 513 µs), which is too coarse for LAN round trips, jitter and the
1 ms tunnel overhead target. `internal/clock` uses QueryPerformanceCounter on
Windows (100 ns resolution) and the Go monotonic clock elsewhere. All RTT
measurements use it.

## D-012 Module path and default relay host

**Decision:** The Go module path is `github.com/NotrinoResearch/lanfolk`,
matching the GitHub account available on the development machine. It can be
changed with one search and replace before the first public release.

The VPS address must never be written to the repository, so there is no
hard-coded default relay host. Release builds inject it at build time from
the local, untracked `.env`. Users can always change it in Settings.
