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

## D-027 Desktop UI: compact main window, separate Settings window, tray, brand

Date: 2026-11-04. Replaces the first desktop UI (a 1100 x 720 window with a
sidebar and every setting on screen), which the product owner found
intimidating.

**Decision**

- **Main window**, 380 x 640, frameless with the native shadow, rounded
  corners and snap layouts (Wails `Frameless` with decorations kept). It shows
  state only: an identity card (power button, status, copyable virtual IP),
  Networks and Friends tabs, live member rows and a Create / Join bar.
  Closing it hides it to the tray; `--minimized` starts it hidden (the Run
  value written by the installer and by "Open at sign-in").
- **Settings window**: the same executable started with `--settings`, with a
  Windows 11 style category sidebar. Wails v2 supports one window per
  process, so a second process is the only way to get a real second window
  without moving to Wails v3 (alpha). Cost: about 50-80 MB while Settings is
  open. Both windows talk to the service, so they stay in sync through its
  events; each has its own single-instance lock.
- **Rows are informational; actions are in menus.** Selecting a member or
  friend extends the row with a readout; copy, remove and "add to network"
  are in the row menu (⋯, right click, Shift+F10). An earlier inline card
  with buttons read as a separate control.
- **Tray**: `fyne.io/systray` (Apache-2.0) on its own locked OS thread (its
  window and message loop live on that thread). Four icon states (online,
  connecting, offline, attention), a menu with status, copy IP, network
  switch, invite, Settings and Quit. Win32 popup menus opt into dark mode
  through uxtheme ordinals 135/136 (Windows 10 1903+). The library loads
  icons at the default (32 px) size and lets the shell scale them, so every
  tray frame is drawn with the small-size geometry.
- **Onboarding** asks only for a name; joining uses the invite code of D-025.
- **Brand**: the mark is four nodes on a diamond joined by three links that
  stop short of the nodes; the green node is "you", the missing link is the
  open seat an invite fills. Cobalt `#2F5BEA` and signal green `#14C38E`.
  Everything is generated from code (`scripts/brand/build.mjs`; wordmark in
  Geist SemiBold, OFL, outlined). Literal, mascot and AI-generated concepts
  were shown and rejected; see `assets/brand/README.md`. Visual rules are in
  `DESIGN.md`.
- **UI language**: English only for now; strings stay in `app.js` and the
  Inno Setup `[Messages]`, ready to extract.
- **Automation build** (`-tags automation`, `scripts/dev/ui.mjs`): a loopback
  API with a random token to run JavaScript in the page, capture the window
  and drive the tray, for scripted UI checks. Release builds use
  `-tags desktop,production,nomock` and contain neither the automation API
  nor the `-mock` preview service.

**Not done**: Mica window material (a transparent window would render badly
on Windows 10; the flat `#f3f3f3` / `#202020` grounds match Mica's tone),
notifications when a member comes online, and a UI translation.

## D-028 Source available under PolyForm Noncommercial 1.0.0

Date: 2026-11-04. Replaces the Apache-2.0 choice of the plan ("Open-source
release").

NotrinoResearch treats Lanfolk as a company product: it must stay free for
players while companies, LAN centers and anyone selling or bundling it need a
commercial license. Apache-2.0 allows all of that for free.

**Decision**: license the code under the PolyForm Noncommercial License 1.0.0
(`LICENSE`, with the `Required Notice` line for NotrinoResearch) and sell
commercial licenses separately (`LICENSING.md`). Contributions need the
Contributor License Agreement (`CLA.md`) so NotrinoResearch can keep licensing
the whole code base.

**Alternatives considered**

- Business Source License 1.1: also restricts commercial use, but turns into
  an open source license on a fixed date. Not wanted for now.
- AGPL-3.0 with a commercial license: real open source, but it allows
  commercial use by anyone who publishes their changes, so it does not meet the
  requirement.
- Keeping Apache-2.0: allows competitors to sell Lanfolk.

**Consequences**

- Lanfolk is source available, not open source; the README and the app say
  so.
- Code published until commit `7272ca9` (tag `apache-2.0-final`) stays
  available under Apache-2.0 to anyone who got it.
- All third-party dependencies are under permissive or file-level licenses
  (MIT, BSD, Apache-2.0, MPL-2.0, the Wintun DLL license, OFL), so the change
  is possible; their notices stay in `THIRD_PARTY_NOTICES.md`.
- The license and the CLA have not been reviewed by a lawyer yet; that is
  listed in `docs/TODO.md`.

## D-029 The default relay is a DNS name

Date: 2026-11-04. Builds from 0.2.1 on use `lanfolk-relay1.notrino.com` as the
default relay instead of an IP address (set in the untracked `build.env`, as
before).

**Why**: the relay can move to another server, for example one with DDoS
protection, by changing the DNS record, without a new release. Clients
resolve the name again every 5 minutes (`transport.RelayClient`). The record
is "DNS only" in Cloudflare: Cloudflare's proxy carries only HTTP(S), so a
proxied record would make the relay unreachable, and proxying UDP (Spectrum)
would also hide the clients' real addresses that hole punching needs.

**Consequences**

- The name does not hide the server's address (DNS returns it) and gives no
  DDoS protection by itself. Protection has to come from the server's
  network; moving the relay to its own server with volumetric DDoS
  protection is listed in `docs/TODO.md`.
- The 0.2.0 release had the address built in; it was withdrawn and replaced
  by 0.2.1. The address was never in the git history.
- Naming further relays `lanfolk-relayN.notrino.com` keeps room for more.

## D-030 Path selection: only fresh paths take over, with hysteresis

Date: 2026-11-04. The first test between two homes (0.2.1) logged about
18,000 direct/relay switches in 30 minutes, one every 50 ms tick, while the
direct path answered in 32 ms and the relay in 158 ms.

**Cause**: a standby path counts as alive for 16 s, the active one for 2 s.
A standby direct path whose last reply was 2-16 s old was selected, was dead
at once as the active path, fell back to the relay and was selected again on
the next tick. The engine also reported the time of the last WireGuard packet
on every tick and that overwrote newer probe replies, which kept the direct
path in that window.

**Rules now** (`internal/pathmgr`):

- Received times only move forward (`OnInbound`).
- A path may replace a working active path only if it answered within
  `DeadAfter` (2 s). When the active path is dead, any path that is alive
  under the standby rule may take over (fast failover).
- A newly active path gets `DeadAfter` from its activation to answer.
- A path that failed needs a new reply before it is used again.
- One score for all paths: RTT, plus `DirectPreference` (5 ms) for relays,
  plus `TCPPenalty` for TCP. Leaving a working path needs a gain of
  `Hysteresis` (2 ms), except from a relay to a direct path, so the old rule
  "direct unless a relay is 5 ms faster" holds exactly.

Not done: an idle mode that probes less without game traffic. Probing the
active path twice a second keeps NAT mappings open and the numbers current,
which matters more for games than the few packets it costs.

## D-031 Nothing slow runs on the packet path or the engine loop

Date: 2026-11-04, from an independent performance review.

- Bookkeeping writes (last seen, path cache) go through
  `store.UpdateLater`: applied to the file in one write within 5 s, never
  blocking. They were saved with fsync on the socket read goroutine (4-8 ms
  of delayed packets per friend per minute).
- Interface addresses are read with one `GetAdaptersAddresses` call (1 ms
  instead of 46 ms) on their own goroutine, at start, on Windows address and
  interface change notifications and every 30 s. Candidates are collected
  every second from the cache, so a new STUN mapping is announced within 1 s.
- The path tick takes no engine lock and allocates nothing in the common
  case; statistics are computed once a second; with no active network the
  tick runs once a second.
- IPC events go through a per-client writer with one pending snapshot per
  event; a UI that stops reading can no longer stop the engine loop. Events
  are not built when nobody listens, and the desktop app does not render the
  per-second peer list while its window is hidden.

## D-032 Update check and notifications live in the desktop app

Date: 2026-11-04.

- The desktop app, not the service, checks for updates: once a day it asks
  the GitHub releases API for the list of releases (generic User-Agent,
  nothing else sent). Settings > General turns it off; About can check by
  hand. Pre-releases count while the app is 0.x or a pre-release. Lanfolk
  does not download or install anything itself: without code signing an
  automatic installer would only add SmartScreen prompts. The installer
  upgrades in place and keeps `C:\ProgramData\Lanfolk`.
- Windows notifications use the tray icon's NIF_INFO (fyne.io/systray has no
  notification API), without sound and with quiet time respected; Windows
  holds them back during full-screen games. They announce changes only: a
  member back after 2 minutes or more, a new member in a network you own, a
  join you started finishing, a new release once. On by default (PRODUCT.md
  principle 4), off in Settings > General.
  The structure is declared in Lanfolk, matching NOTIFYICONDATAW byte for
  byte (a test checks the offsets): a wrong layout makes Windows accept the
  call and show nothing, which is how 0.3.0 shipped.
- Hello carries the sender's release as an optional trailing field (since
  0.3.0; 0.2 ignores it), so the app can say "Older Lanfolk" on a member and
  suggest updating when a member is newer.

## D-033 Members reach desktop programs, not Windows services

Date: 2026-11-04.

A security review found that the virtual LAN exposed more of a member's
computer than a game needs, and that some local protections were weaker than
intended. The fixes keep the promise "install, share a code, play": none of
them adds a prompt, and none changes the wire protocol between clients.

- **Service filter.** The firewall rule that lets members in for any program
  stays (it is what makes games work without firewall prompts), but the
  tunnel now drops inbound UDP and new TCP connections to a local port that
  only session-0 processes own: Windows services and the System process
  (HTTP.sys, SMB). Remote access tools, media and database servers that run
  as services are therefore out of reach, while games and anything else the
  user runs on the desktop stay reachable. The port owner table is rebuilt in
  the background at most once a second while traffic arrives; the packet path
  does an atomic load and a map lookup (about 8 ns). A port not in the table
  is allowed, so a game that just opened a port never waits. Settings >
  Troubleshooting has "Let members reach Windows services" for the rare game
  server that runs as a service; the diagnostic report counts drops
  (`DroppedService`).
- **Denylist.** Added LLMNR, SSDP, the UPnP device host, WS-Discovery,
  Windows Media sharing, RPC over HTTP and UDP remote desktop. These also stop
  name resolution and discovery multicast from reaching other members. mDNS
  (UDP 5353) is not on the list: a game that listens on it receives it, while
  Windows' own mDNS responder, a service, does not (service filter).
- **Data folder.** `C:\ProgramData\Lanfolk` inherited an ACL that let every
  local user read it and create files in it. The service now gives it a
  protected ACL (LocalSystem and Administrators only, owned by
  Administrators) at install and at every start, removes links it must never
  follow, and takes the folder over if someone else owns it. The app never
  reads the folder directly, so nothing changes for users; reading the logs
  needs administrator rights.
- **Control pipe.** Interactive users can no longer create server instances
  of the service's pipe; clients check that the pipe belongs to the service;
  the service identifies clients by impersonation instead of by process ID
  (PROTOCOL.md 11.1).
- **Invites.** Removing a member also drops the network's invite code, and
  friendships that only an invite created end when no network is shared any
  more (PROTOCOL.md 10.3). The owner's "joined" notification says how to
  remove an unexpected member.
- **Diagnostic report.** Public IP addresses are shortened to their first
  half, keys to key prefixes, and the friend code is left out, because users
  attach the report to public issues. It now includes the tunnel counters.
- **Relay.** The control rate limiter has a fixed size and can no longer be
  filled by a flood of spoofed addresses; data is limited per IP and in
  total; TCP connections are limited in total; silent TCP connections without
  a session are closed; delayed echoes are limited per IP; IPv6 sources count
  per /64; and while the relay is under pressure, a Register that would
  create state must carry a cookie (PROTOCOL.md 4.3.1, 4.5). Clients before
  0.4 ignore the cookie request, so they cannot open new UDP sessions while a
  relay is under attack; their existing sessions and the TCP fallback keep
  working.

## D-034 Captured broadcasts get a fresh UDP checksum

Date: 2026-11-05.

Commandos 2 (DirectPlay 4) found no sessions between two homes in the default
capture mode. DirectPlay sends its session enumeration from a socket bound to
`0.0.0.0` to `255.255.255.255:47624`, so Windows sends it out of the
physical adapter and Lanfolk picks up the loopback copy on `127.0.0.1` with
`SIO_RCVALL` (capture mode). That copy is taken before checksum offload has
run: its UDP checksum field holds only the folded pseudo-header sum that the
network card is meant to complete. Lanfolk rewrote the source address with an
incremental checksum update, which keeps a wrong checksum wrong, and the
receiving Windows dropped every captured datagram silently. This is also why
`lanprobe test` showed "Global bcast (0.0.0.0)" as FAIL without compatibility
mode (MEASUREMENTS.md); it was taken for a Windows routing limit.

Captured datagrams now get their UDP checksum computed from scratch after the
source rewrite (a zero field, "no checksum", stays zero). The cost is one pass
over the datagram for captured broadcasts only; packets read from the virtual
adapter keep the incremental update. A unit test feeds a datagram with the
offload placeholder and checks that the delivered copy has a valid checksum.

Checked between two homes (one machine on a phone hotspot, the other on home
fiber) with Commandos 2: before the fix no session was listed in capture
mode; with it the session is listed and joining, the lobby and commando
selection work. Compatibility mode worked before and after.

DirectPlay 4 takes the peer addresses from the packet sources and does not
carry physical IP addresses in its payload, so the "IP address in the
payload" class did not apply.

## D-035 Relay only holds for both directions; WireGuard does not roam

Date: 2026-11-05.

SETUP.md promises that with **Relay only** other members see only the relay's
address. The two-home test matrix showed that this held only for the traffic
the setting's owner sent. When B turned Relay only on, B moved to the relay,
but A kept the direct path it already had. B still answered A's direct
probes from its home address. A listed the path as direct (55 ms), and B
listed it as relayed. A member that learned B's address before (from earlier
candidates or the path cache) kept using it.

The setting now holds in both directions:

- A Relay only client drops control frames and WireGuard packets that
  arrive on a direct path, so it never answers from its home address.
- The Hello flag "relay only" is honoured: the path manager drops all direct
  paths to such a peer, including the active one, and adds none while the
  flag is set. Turning the setting on locally drops them the same way.
  Before, the "keep sending on the last path" rule could keep a dead direct
  path.
- A change of the setting sends a Hello to every member at once, and
  candidates go out again without waiting for the one-minute repeat, so
  both sides switch within seconds in either direction.
- WireGuard roaming is off (`DisableSomeRoamingForBrokenMobileSemantics`).
  Every WireGuard peer's endpoint names the peer, and the path manager picks
  the address at send time. Roaming to the source of an authenticated
  packet bypassed path selection, and with it Relay only.

An engine test turns Relay only on at one side of a working direct pair.
Both sides must then use the relay, and no packet may pass between the two
home addresses for three seconds. Turning the setting off must bring the
direct path back. The test fails on the code before this change.

## D-036 A peer's candidates restart failed direct paths

Date: 2026-11-05.

In the two-home matrix the direct path twice came back only after 7.5
minutes. The service log on the home-fiber machine showed the cause.
That is the sum of the 30, 60, 120 and 240 s retries. Once a direct path
had failed (here after a settings change on both sides), nothing restarted
it early: Candidates from the peer, sent on a settings change and once a
minute, were ignored for addresses already known. The hole through the
phone line's NAT opens only when both sides probe at the same time, and the
two sides retried on unrelated schedules.

A Candidates message now restarts the probe burst on every direct path that
does not answer, when no direct path is active, and the receiver answers with
its own candidates so that both sides probe together. This is ICE's rule of
reacting to the peer's checks instead of waiting for one's own timer. A
limit of once per 5 s per peer prevents a ping-pong. With no direct path
possible (two hard NATs), the cost is one 3 s burst per minute.

An engine test blocks the direct path until both sides have failed it
(22 s), unblocks it and lets one side send its candidates: both must be
direct again within 6 s. It fails on the code before this change. The plan's
item 2.4 (faster return after a short blip without any message from the
peer) stays open.

## D-037 Announced nicknames survive a restart

Date: 2026-11-05.

The signed member list keeps the nickname a member had when they joined; the
nickname a member announces later (Hello) was kept only in memory (D-030).
After the service restarted, a friend who had renamed himself showed his
first name ("Player-241D") again until he came back online. Restarts are
normal (Windows updates, a Lanfolk update, a reboot), so users saw wrong
names for offline members.

The last announced nickname of each peer is now stored (`known_names`, at
most 512, the oldest seen go first) and loaded as the peer's current name at
start. Member lists, friends and connections show it; the member list
nickname stays the fallback for members that never announced one to us. The
store is written only when the name changes. Network names did not have this
problem: a rename is a new version of the signed member list, which is
stored and sent to every member.

An engine test renames a member, restarts the other side with the member
offline, and checks the member and friend lists. It fails on the code before
this change. Names that were never stored (before 0.4.2) appear after the
member's first connection.

## D-038 DirectPlay announces the virtual LAN, not the member's router

Date: 2026-11-05.

The second two-home test with osman (Lanfolk 0.4.2 on both sides, direct
path, 22 ms) still showed no Commandos 2 session. A packet capture on the
host showed osman's DirectPlay session requests arriving through Lanfolk.
Each request carried a reply address of `10.229.176.218:22217`: the
external address of osman's router (inside his provider's CGNAT) and a port
DirectPlay had forwarded there with UPnP. The host answered that address
over the internet, where it is unreachable. This also explains the first
report ("the host tried TCP to 10.229.x.x:22217").

Reverse engineering of the Windows DirectPlay components (Ghidra, on this
machine's `dpwsockx.dll` and `dpnathlp.dll`) showed the mechanism.
`dpnhupnp.dll` and `dpnhpast.dll` are 10 KB stubs; the NAT helper is
`dpnathlp.dll`, which the TCP/IP service provider loads. It sends unicast
SSDP searches to the default gateway, asks for the external address, maps
a port, and DirectPlay writes that address into its messages. The helper
reads `HKCU\Software\Microsoft\DirectPlayNATHelp\DPNHUPnP\UPnPMode` from
the current user's hive, not the machine's. 1 means always use UPnP, 2
means never, anything else means use it unless the SSDP or UPnP host
service is disabled. The helper also keeps its mappings in
`...\DPNHUPnP\ActiveNATMappings`, which is why the port stayed 22217.
Changing the machine-wide keys (`HKLM`, the provider's `Gateway` value)
had no effect.

**Decision.** While a network is active, the service keeps `UPnPMode = 2`
for the owner account (the account recorded for the control pipe, D-033),
in `HKU\<SID>`. When no network is active, and when the service stops, it
restores the value it replaced. The replaced value is kept in
`directplay-upnp.json` in the data folder, so the restore also works after
a restart. A value that the account changed itself in the meantime is left
alone. An account that is signed out is handled when its registry is
loaded again. The service checks every 2 s; a check without a change reads
one registry value.

This is not a game rule. DirectPlay is a Windows component, and the setting
applies to every DirectPlay game. The cost is that while a Lanfolk network
is active, DirectPlay games cannot open router ports over UPnP for play
outside Lanfolk. Lanfolk provides the connection they need for LAN play.

Considered and rejected:
- Asking users to turn off UPnP on their router (it also removes Lanfolk's
  own port mapping, and users will not do it).
- Changing the setting by hand.
- Rewriting the address inside DirectPlay messages in the tunnel (a
  protocol parser in the packet path, and the port would also need the
  router's mapping).

Lab reproduction (no second person needed): a Hyper-V VM behind a fake UPnP
router on this PC (`fakeigd.py` answers SSDP and the WANIPConnection SOAP
actions with external address 10.229.1.2). Without the change, the VM's
DirectPlay mapped TCP 24197, its requests carried `10.229.1.2:24197` and
the session list stayed empty. With the 0.4.3 development service, the VM
sent no UPnP request and its requests carried `0.0.0.0:2300`. The session
was listed and the join reached both lobbies. Turning the network off
restored the account's value, and turning it on again set it to 2. Unit tests
cover unset and set values, a restart in between, a value changed by the
user, a reset while active and a signed-out account.
