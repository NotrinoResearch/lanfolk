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

## D-013 DHT runs only while no relay is reachable

**Plan:** Read the Mainline DHT record when the relay is unreachable; publish
on change and at least hourly.

**Decision (user choice):** The DHT fallback is a setting, on by default, but
the DHT node starts only after no shared relay has been reachable for 30
seconds and stops when a relay comes back. While it runs it is a passive,
read-only client (BEP 43: it does not answer other nodes' queries) on its own
random port. Records are published and looked up only during the outage.

**Why:** Encryption protects record contents and integrity, and the DHT is
not in the trust path: a forged or replayed record can only waste probes,
because paths and tunnels are authenticated with the pair keys and
WireGuard. The remaining risks are not about content:

- DHT nodes see the user's IP, and the nodes that store a record see both the
  publisher's and the reader's IP, which links the two for a day.
- The DHT library parses untrusted internet traffic inside the service, which
  runs as LocalSystem. A torrent client has the same exposure but runs with
  user rights.
- Networks may classify the traffic as BitTorrent.

Running the DHT only during relay outages keeps that exposure rare while
keeping an automatic fallback for the single relay that exists today.

**Open risk:** Moving the DHT client into a low-privilege helper process
would remove the LocalSystem exposure. Revisit when a second relay exists,
since the DHT then adds little.

## D-014 DHT keys rotate every UTC day

**Decision:** The BEP 44 salt, signing key and encryption key are derived
from the pair secret and the UTC day (`identity.Pair.ForDHTEpoch`). Both the
target and the visible signing key change daily, so storing nodes cannot
follow a pair across days. Lookups try today and yesterday. A record more
than about a day old cannot be opened at all.

## D-015 Layered relay list with registration at every relay

**Plan:** One shared relay; "additional relays can be added later".

**Decision:** Two peers can only meet at a relay where both are registered,
so the relay list must be shared. The effective list is, in order:

1. The default relay injected at build time (an IP address for now; DNS
   names are also supported).
2. Up to three relays chosen by the network owner. They travel inside the
   owner-signed member list, so members cannot change them.
3. Relays the user adds in Settings.

A client registers at up to three relays at once. The path manager keeps
one shared relay path per relay and picks the lowest RTT. The first relay is
used until RTTs are known.

**Comparison:** RustDesk's ID server (hbbs) holds the relay pool, checks it
every 3 s and assigns relays round-robin, without looking at latency.
Lanfolk has no central ID server, and games need the fastest relay, so the
owner-signed list provides consistency and selection is by measured RTT.
A signed, remotely updatable default list is a possible later addition.

## D-016 The virtual-LAN firewall rule also allows broadcast and multicast

**Decision:** The inbound allow rule for the Lanfolk adapter lists local
addresses 10.77.0.0/24, 255.255.255.255 and 224.0.0.0/4. Broadcast and
multicast packets that Lanfolk writes to the adapter are not addressed to
the member's own IP and would otherwise be dropped. The remote address is
still limited to 10.77.0.0/24, and the denylist block rules still win.

## D-017 Process notes

- The `internal/discovery` package was written by a sub-agent that wrote
  its tests after the code, against the tests-first rule. The tests were
  reviewed during the merge and extended for daily key rotation.
- Commit `55d275c` contained a failing `peermsg` limit test. A shell pipeline
  hid the test's exit code. The fix is commit `5e3fff1`. The local shell now
  uses `pipefail` so that a failing test stops the commit.

## D-018 natprobe limits

- Filtering behavior (address- or port-dependent) is not measured. That
  needs a server that answers from a second address, and the relay has one.
- `stun.l.google.com` and `stun1.l.google.com` can resolve to the same IPv4
  address, so natprobe also uses `stun.cloudflare.com` to compare mappings
  across different destinations.

## D-019 Local data in one JSON file

**Plan:** The local data model lists tables (identity, friends, networks,
members, peer_cache, settings).

**Decision:** The tables are arrays and objects in one JSON file
(`%ProgramData%\Lanfolk\lanfolk.json`), replaced atomically on every
change. The data is small (a few hundred records at most), and a database
would add a large dependency. A damaged file is renamed aside and a fresh one
is started, so the service always comes up. The plan's settings fields
`relay_host`, `relay_udp_port` and `relay_tcp_port` became the custom relay
list (D-015).

## D-020 Tests after code for the engine, service and CLI

The engine, the service and the CLI were written before their end-to-end
tests, against the tests-first rule. The tests followed in the same session
before anything was committed. The tunwrap and UI packages were written tests
first.

## D-021 Relay socket queues and readers

**Decision:** The relay's UDP socket buffers are 1 MB instead of 4 MB. Under
overload the larger buffers queued packets for about 290 ms; now they are
dropped instead, as the plan's low-latency rules ask. Extra reader goroutines
can be enabled with `RELAY_UDP_READERS`. The default stays 1, because extra
readers did not raise throughput on the development machine and can reorder
packets of one flow. See the load results in MEASUREMENTS.md.

## D-022 The installer is a 32-bit NSIS program (superseded by D-026)

Official NSIS builds produce 32-bit installers. The installer requires 64-bit
Windows 10 or later and installs the 64-bit binaries into the 64-bit Program
Files folder.

## D-023 Control pipe access

The pipe's security descriptor allows LocalSystem, Administrators and
interactive users. On top of that, the service accepts only the user who ran
`lanfolk-service install` (recorded as the owner) and elevated
administrators. If no owner was recorded, the first client becomes the owner.

## D-024 Development flags of the service

`lanfolk-service run` accepts `-listen`, `-loopback-candidates`, `-relay`
and `-no-tun`. They let several instances run on one machine without
administrator rights, without a firewall prompt and against a local relay.
The NAT simulation uses them on Linux.

## D-025 Joining a network with one invite code

**Problem:** joining took three manual steps: both people exchanged friend
codes, waited until the friendship was active, and then the owner added the
member. For a game night that is too much; Hamachi needs only a network name
and a password.

**Decision:** the owner presses "Invite" and gets one code (`lanfolk-i1...`,
docs/PROTOCOL.md section 10.3). The friend pastes it and is a member a few
seconds later, with no further action on either side. The code holds the owner's
key, the network ID, a 128-bit random token and an expiry (7 days). It is
reusable until it expires or the owner resets it. There is one invite per
owned network, stored with the owner's data.

**Design and why:**

- *Rendezvous on the shared relay.* A joiner has no friendship with the owner
  yet, so there is no pair session to talk in. From the token both sides derive
  an invite session ID and a symmetric key (HKDF, labels under
  `lanfolk/v1/invite`) and meet in that session at the relay, which already
  does exactly this for pairs and needs no change. The relay learns nothing: it
  sees a random session ID, not an identity. The alternatives were a new
  server-side mailbox (state and a new trust point on the relay) or putting the
  owner's candidate addresses into the code (they go stale in minutes and a
  code lives for days).
- *The owner has to be online.* Nobody else can add a member: the member list
  is signed by the owner's key, and the first Hello between the two has to reach
  the owner anyway. Making joining work with the owner offline would need a
  relay that stores requests, which is a different trust model. The joiner waits
  and retries every 3 s until the invite expires, and the pending join survives
  a restart of the service, so "owner comes online later" works; the UI shows
  "Waiting for the owner to come online".
- *Signed messages.* JoinRequest is signed by the joiner (proof that the
  sender owns the key being added) and JoinAccepted by the owner. The token only
  decides who may knock; it is not trusted for identity. Without the owner's
  signature, someone who holds a leaked code could answer a joiner while the
  real owner is offline and make it stop waiting.
- *Use the friend machinery for the rest.* The owner stores the joiner as a
  friend with the network pending, the joiner stores the owner as a friend at
  once, and the existing mutual Hello with the friend flag confirms the
  friendship. When it is confirmed, the owner adds the member
  (`AddMember`) and the existing list push, gossip and auto-activation do the
  rest. So no new trust rule exists: a new network is still accepted only from
  an active friend who owns it, and a friend can join only with a Hello it
  could not have forged. The side effect is that both become friends, which is
  what the users expect anyway.
- *Relay only.* Direct paths need candidates, which the two sides exchange only
  after they know each other. An invite exchange is a few small frames, so the
  relay is fast enough. If no relay is reachable, redeeming keeps trying. The
  code carries no relay hints, so owner and joiner meet at a relay both use,
  normally the build's default relay; an owner whose only relay is a private one
  cannot be joined by invite until the friend adds that relay as a custom
  relay.
- *Two parties per session.* The relay session holds the owner and one joiner.
  A second joiner at the same moment gets `SessionFull`; the joiner registers
  the session again every 3 s and gets in as soon as the first one has
  unregistered after its acceptance. A clean exit now unregisters all sessions
  (pair sessions too), because a restarted owner otherwise waited 30 s for the
  stale registration of its previous run to expire.

**Security:**

- The token is a bearer secret, like a Hamachi password. Whoever has the code
  can ask to join while the owner is online, so share it only with people you
  would add anyway. The owner is not asked per request; that is the point.
- Limits: the code expires after 7 days; the owner can reset it at any time,
  which makes the old code useless at once (nobody serves its session) but keeps
  people who joined already; at most 30 new joiners per invite per hour; the
  request timestamp must be within 10 minutes; a full network refuses.
- The owner can remove any member as before. A removed member who still has a
  valid code can ask again, so remove-and-reset is the way to lock someone out.
- A holder of the code can do no more than become a member, which is what
  the owner meant to allow; it cannot change the list, remove anyone or act as
  the owner.
- The invite key protects the exchange only against outsiders (the relay and
  the network); the joiner's nickname and key are visible to anyone with the
  code. That is acceptable for a bearer secret that is shared in a chat.

**Not done:** per-request approval by the owner, a per-person single-use code,
and joining while the owner is offline (see above).

## D-026 Inno Setup instead of NSIS

Supersedes D-022. The installer is built with Inno Setup 6
(`installer/lanfolk.iss`) instead of NSIS. Reasons:

- `WizardStyle=modern dynamic` gives a current-looking wizard that follows the
  Windows light or dark setting. NSIS Modern UI 2 looks dated and has no dark
  mode.
- It is a native 64-bit installer (`ArchitecturesInstallIn64BitMode=x64compatible`),
  so no 32-bit installer runs in WOW64 and no file system or registry
  redirection has to be handled by hand.
- The wizard art is PNG with transparency and several sizes (`@2x`), chosen by
  the display scale. NSIS needs BMP files.
- Tasks (start with Windows, desktop shortcut, PATH), the Apps entry, upgrades
  and the uninstaller are built in or short Pascal code; the NSIS script
  needed hand-written logic for each.
- New: a PATH task for the command-line tools (`lanprobe`, `natprobe`,
  `lanfolk-cli`). Users could not run them from a shell before.

Behavior is otherwise unchanged: administrator, Windows 10 or later, 64-bit
only; `Program Files\Lanfolk`; the old UI is closed and the old service removed
before files are replaced; `lanfolk-service install` and `start` after the
copy (an install failure is shown, the finish page says so and the exit code is
3; a start failure is a warning); the data folder in `ProgramData\Lanfolk` is
kept unless the user answers Yes, and silent uninstalls always keep it. An
install made by the NSIS installer is migrated (its Apps entry, uninstaller and
Start Menu folder are removed). The auto-start value
(`"Lanfolk.exe" --minimized`) is written to the HKCU of the account that runs
the installer, as before.

Inno Setup 6.7.4 was installed per user without administrator rights
(`innosetup-6.7.4.exe /VERYSILENT /CURRENTUSER /NORESTART`) from
`https://github.com/jrsoftware/issrc/releases/download/is-6_7_4/innosetup-6.7.4.exe`,
the download linked from jrsoftware.org/isdl.php. The file is Authenticode
signed (Pyrsys B.V., signature valid). SHA-256 computed on 2026-11-04:
`A549929319F9120716677C685535CA4671E4B937B5FDC87E2585F32D8EB30E8C`.
Dynamic dark mode needs Inno Setup 6.6 or newer.
