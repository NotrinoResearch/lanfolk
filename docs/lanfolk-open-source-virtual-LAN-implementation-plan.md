# Lanfolk: Open-Source Virtual LAN Implementation Plan

Nov 4, 2026 · @Çağın Polat

## What is Lanfolk?

**Lanfolk is an open-source virtual network application that lets friends play LAN games over the internet as if they shared a local network.** Its goal is to let players behind CGNAT, who cannot open ports, join low-latency games without changing router settings.

Users add friends, create a private network and start a game over LAN. Lanfolk is not a VPN for routing internet traffic. It does not provide file sharing, printer sharing or location changing.

## Summary

**Lanfolk must connect players even when every player is behind CGNAT.** A direct connection is always preferred. If it fails, traffic uses another network member or the shared relay running on the user's VPS.

- **Method:** Wintun, the WireGuard protocol, UDP hole punching, member relays, a shared relay and BitTorrent DHT as a fallback.
- **User experience:** Users do not run servers or forward ports. They only open the application.
- **Shared relay:** It runs in Docker on a fixed-IP VPS in Germany. It forwards encrypted packets and stores no user accounts.
- **Connection guarantee:** A connection succeeds when the relay is running and the line can reach it over UDP or TCP.
- **Latency limit:** Relayed traffic experiences the sum of both players' latency to the VPS. This must be measured on real lines.
- **Scope:** The system is designed around LAN network behavior, not specific games.

No separate Hamachi research is required. All methods use open standards and open-source components.

## Requirements

**The target experience is close to Hamachi: friends add each other, create a private network and join a game over LAN.**

### User scenario

1. Ahmet, Mehmet and Veli install Lanfolk. Their identities are generated automatically.
2. They send each other friend codes and enter them in the application.
3. Ahmet creates a **New Network** and adds Mehmet and Veli.
4. When all three open the application, they join the network automatically.
5. They start a game and connect over LAN.

| Request | Decision | Acceptance criterion |
| --- | --- | --- |
| Hamachi-like virtual network | Each member gets a virtual IP and a Windows virtual adapter | Members can ping each other by virtual IP |
| General-purpose LAN gaming | No game-specific rules; unicast, broadcast and multicast are carried | `lanprobe` passes every supported behavior |
| Friend list | Friendship is created by entering codes on both sides | Friendship activates after both sides enter codes |
| Nickname and identifier | Display nickname plus a friend code containing the public key | Mismatched keys are rejected |
| Private network | The owner adds friends; members connect in a full mesh | Non-members cannot join |
| Users do not run servers | The shared relay runs in Docker on the VPS | No user action is needed beyond client setup |
| No port forwarding | Direct path, member relay and shared relay are tried automatically | A connection works when every member is behind CGNAT |
| Connection visibility | Each member shows path type, ping, jitter, loss and quality | Path and quality changes update in the UI within seconds |
| Optional port opening | UPnP, NAT-PMP, PCP and manual port support | Normal flow works with these settings disabled |
| Low latency | Tunnel overhead is bounded | Direct-path overhead is at most 1 ms p50 and 3 ms p99 |
| Small-packet priority | No retransmission or buffering; queues stay short | Added loss is below 0.1% |
| Minimal scope | File and printer sharing ports are closed by default | Members cannot reach SMB or RDP ports |

### Why MAC addresses are not used

- **Not visible over the internet:** A MAC address stays on the local network and does not cross routers.
- **Can be spoofed:** It can be changed on Windows, so it does not prove identity.
- **Not stable:** Wired, wireless and virtual adapters have different addresses.
- **Cannot locate a user:** An internet address cannot be derived from a MAC address.

Lanfolk uses friend codes instead. A code is the text form of a 32-byte Ed25519 public key. It cannot be shortened because there is no central user registry and the full key is needed to find a peer. Users copy the code and send it through a messaging app. A nickname is display text only.

## Architecture

**The system has two runnable parts: the Windows client and a relay running in Docker.** Game traffic is end-to-end encrypted. The first client platform is Windows 10/11 x64. The relay platform is Linux.

&#91;embedded content: system architecture · direct path with relay fallbacks\]

Veli relays traffic only when two members cannot connect directly. The shared relay is both the final fallback path and a fast signaling channel.

| Part | Responsibility | Technology |
| --- | --- | --- |
| Lanfolk service | Adapter, tunnel, discovery, path selection, LAN behavior, member relay | Go, wireguard-go, Wintun |
| Desktop UI | Friends, networks, status and settings | Wails (WebView2) |
| Shared relay | STUN, signaling and final fallback data path | Go, UDP, TCP 443, Docker |
| Fallback discovery | Peer discovery when the relay is unavailable | BitTorrent Mainline DHT, anacrolix/dht |

### Core decisions

- **Use Go:** wireguard-go, Wintun integration and DHT libraries are available in one language. High throughput is not the priority.
- **Use Wintun:** A custom kernel driver requires EV signing and Microsoft submission. Wintun ships as a signed DLL.
- **The network owner is the administrator:** The owner manages membership and IP allocation. Members keep working from the latest signed list.
- **The relay has no accounts:** It forwards packets by session ID. It cannot see friend lists, network membership or game content.
- **Use one UDP socket:** STUN, probes, relay and WireGuard share one port, so the hole-punched path is the same path used by the tunnel.
- **Keep extension points:** Packet device, transport and relay address sit behind interfaces. TAP or additional relays can be added later.

### Repository layout

```
cmd/lanfolk-service  Windows service
cmd/lanfolk-cli      CLI and diagnostics
cmd/lanfolk-relay    Shared relay
cmd/natprobe         NAT and DHT measurement tool
cmd/lanprobe         LAN behavior test tool
internal/identity    Keys and friend codes
internal/network     Membership and virtual IPs
internal/discovery   Relay signaling, DHT and cache
internal/pathmgr     Candidates, probes and path selection
internal/transport   Custom wireguard-go Bind
internal/peerrelay   Member relay
internal/tunwrap     Wintun, filters, broadcast and multicast
internal/winnet      IP, metric and firewall integration
ui/                  Wails UI
deploy/relay/        Dockerfile, compose file and .env example
docs/                Protocol, setup and compatibility docs
```

### Relay Docker configuration

| Variable | Value | Description |
| --- | --- | --- |
| `RELAY_PUBLIC_HOST` | `ip` from `.env` | Address clients connect to |
| `RELAY_UDP_PORT` | `3478` | STUN, signaling and UDP data |
| `RELAY_TCP_PORT` | `443` | TCP fallback; choose another free port if occupied |
| `RELAY_MAX_SESSION_KBPS` | `2000` | Per-session rate limit |
| `RELAY_METRICS_ADDR` | `127.0.0.1:9100` | Local status and metrics endpoint |

The compose project name is `lanfolk-relay`. The container uses `network_mode: host`, `restart: unless-stopped`, a read-only filesystem and bounded logs. Host networking prevents Docker from creating extra port-forwarding rules, so ports are checked before deployment. The container runs as a non-root user.

## Identity, friends and network model

**There are no accounts or central user database; identity is a key pair generated on the device.** Friend and network data are stored on the client. Users can export their identity as a backup.

### Identity and friendship

1. On first launch, generate an Ed25519 identity key and protect it with Windows DPAPI.
2. A friend code is the public key encoded as z-base32 with the `lanfolk1` prefix.
3. Friendship becomes active after both sides enter each other's code. One-sided adding grants no access.
4. Derive a pair secret with X25519 key agreement and HKDF.
5. Derive DHT, session ID and signaling encryption keys from that secret.

### Private network model

- A user is active in one network at a time. This keeps one adapter and one address range.
- The creator becomes the **owner** and signs the member list with their identity key.
- The owner can add only their friends. Members do not need to be friends with each other.
- Each member gets a stable virtual IP in `10.77.0.0/24`, starting at `10.77.0.2`. Up to 253 members are supported.
- Members store the latest signed list. Existing members can connect while the owner is offline, but membership cannot change.
- A signed list with a higher version replaces older lists. Removed members lose tunnels.

`10.77.0.0/24` is a private range. Do not use `100.64.0.0/10`, because it can conflict with CGNAT line addresses.

### Local data model

| Table | Fields |
| --- | --- |
| `identity` | `public_key`, `encrypted_private_key`, `nickname` |
| `friends` | `public_key`, `nickname`, `added_at`, `last_seen_at` |
| `networks` | `id`, `name`, `owner_key`, `list_version`, `owner_signature`, `active` |
| `members` | `network_id`, `public_key`, `nickname`, `virtual_ip` |
| `peer_cache` | `public_key`, `address`, `path_type`, `last_success_at` |
| `settings` | `relay_host`, `relay_udp_port`, `relay_tcp_port`, `compat_mode`, `manual_port` |

### Peer messages

| Message | Purpose |
| --- | --- |
| `Hello` | Protocol version, nickname and capabilities |
| `MembershipUpdate` | Distribute the signed member list |
| `Candidates` | Share current candidate addresses |
| `PathProbe`, `PathProbeAck` | Test paths and measure RTT |
| `RelayOffer` | Offer to act as a member relay |
| `LanFrame` | Carry broadcast and multicast packets |

## Connection setup

**A connection starts through the shared relay and moves to a better path when one is found.** Users do not wait, and the connection survives if a direct path fails.

&#91;embedded content: path selection · shared relay stays as fallback\]

The shared relay path stays alive as a fallback. If the selected path fails, traffic returns to it automatically.

### Peer discovery order

1. Try cached last-known-good addresses.
2. Get candidates from connected network members.
3. Send encrypted `Candidates` through the shared relay.
4. If the relay is unreachable, read the Mainline DHT record.
5. If all fail, offer a time-limited connection code to share manually.

DHT records use BEP 44 mutable items. Salt and encryption keys are derived from the pair secret. Record contents are encrypted and the encoded value must not exceed 1000 bytes. Publish on change and at least hourly.

### Candidate addresses

| Candidate | Source | Use |
| --- | --- | --- |
| Local IP | Network interfaces | Same home or local network |
| Public IPv6 | Network interfaces | Both sides have IPv6 |
| STUN address | Relay and public STUN | Hole punching |
| Automatic port | UPnP, NAT-PMP, PCP | Line has a real public IP |
| Manual port | User setting | User chooses to open a port |
| Member relay | Connected member | No direct path |
| Shared relay | Docker relay | Final path in all cases |

### Path selection rules

1. Connect to the relay using a session ID shared with the peer.
2. Exchange encrypted candidates through the relay.
3. Send probes every 250 ms for 3 seconds from both sides.
4. Select the lowest RTT working path. Prefer a direct path unless the relay is at least 5 ms better.
5. If no direct path exists, members directly connected to both peers become relay candidates. Use the lowest total RTT.
6. If the selected path is silent for 2 seconds, switch to the fallback path.
7. Send keepalives at least every 15 seconds on the active path. Update this from `natprobe` results.
8. On network changes, recollect candidates and restart probes.

### Relay protocol

- Each packet carries a 16-byte session ID derived from a secret known only to the two peers.
- The relay forwards packets between two clients registered with the same session ID. It does not decrypt payloads.
- Sessions are limited by per-IP session count, per-session rate and idle timeout.
- The relay stores no persistent user data or traffic content.
- If UDP is blocked, carry the same session over TCP. Show the higher-latency state in the UI.
- Define UDP and TCP message formats in versioned `docs/PROTOCOL.md`.

### Hard NAT

If the external port changes by destination, normal hole punching may fail. Lanfolk then uses a member relay, followed by the shared relay. Add port prediction or birthday methods only if real measurements show a benefit.

### Users who can open ports

No setting is required by default. UPnP, NAT-PMP and PCP are tried automatically where supported. Users can enter a fixed UDP port. This improves direct connectivity and member relay availability.

### Connection information shown to users

The UI shows the following live data for each member.

| Field | Display |
| --- | --- |
| Path type | Direct, Member relay, Shared relay or TCP fallback |
| Ping | Latest RTT and 60-second average |
| Jitter | RTT variation over 60 seconds |
| Packet loss | Percentage from probes over 60 seconds |
| Quality | Good, Fair or Poor |
| Relay details | Relaying member nickname or shared relay name |

Initial thresholds: Good means ping up to 60 ms, jitter up to 10 ms and loss up to 0.5%. Poor means ping above 120 ms, jitter above 30 ms or loss above 2%. Everything else is Fair. Store thresholds in configuration so measurements can refine them.

## Low-latency design

**Tunnel overhead must be small; the network path determines most of the ping.** Measure tunnel overhead and path latency separately.

| Measurement | Target | Status |
| --- | --- | --- |
| Tunnel overhead, direct path | At most 1 ms p50 and 3 ms p99 | Measured in Phase 1 |
| Shared relay latency | Sum of both players' VPS latency plus at most 2 ms | Measured in Phase 2 |
| Path switch interruption | At most 2 seconds | Target |
| Virtual MTU | 1380 bytes | Design value |

### Per-packet overhead

WireGuard adds 32 bytes per data packet, or 60 bytes with UDP and IPv4. Relay paths add 16 more bytes. A game sending 64 packets per second adds 3,840 bytes per second in one direction, about 31 kbit/s. This fits the low-bandwidth target.

MTU calculation: 1492 bytes for PPPoE minus IPv6 (40), UDP (8), WireGuard (32) and relay header (16) leaves 1396. Use 1380 as a safety margin.

### Implementation rules

- Do not batch packets. Send immediately.
- Keep UDP and Wintun queues short. Dropping a late packet is better than delivering it late.
- Do not retransmit in the tunnel. Let the game protocol decide.
- Avoid allocations on the data path. Use buffer pools.
- Run the service at high priority. ChaCha20-Poly1305 is lightweight for small packets.

### Optional small-packet protection

A **Dual path** mode may be added in v1.1. Small packets travel over both direct and relay paths, and the receiver drops duplicates. Enable it only if Phase 9 measurements justify the extra bandwidth.

## General LAN compatibility

**Lanfolk does not know games; it reproduces local network behavior.** The application contains no game names, profiles or game-specific port lists. The goal is for IPv4 LAN games to work without extra configuration.

### Supported LAN behaviors

| Behavior | Where it appears | Lanfolk solution | Scope |
| --- | --- | --- | --- |
| TCP and UDP unicast | Game connections | All ports use the virtual IP | First release |
| Subnet broadcast | LAN game discovery | `10.77.0.255` reaches the adapter and is copied to online peers | First release |
| Global broadcast | Server browsers | Capture `255.255.255.255` or use compatibility mode | First release, verified in Phase 0 |
| Multicast | Local discovery protocols | Copy adapter group traffic to peers | First release, verified in Phase 0 |
| IP in payload | Protocols announcing their own address | Compatibility mode helps select the virtual IP | First release |
| Private IP checks | Games accepting only LAN addresses | `10.77.0.0/24` is private | First release |
| Peer-to-peer game traffic | Serverless games | Network members use a full mesh | First release |
| Internet authentication | Game accounts or licenses | Tunnel does not touch normal internet traffic | Game dependent |
| Non-IP protocols | Older IPX and raw Ethernet games | Layer 3 cannot carry them | Outside first release |

Local discovery is the main compatibility risk. Windows sends `255.255.255.255` packets only through the preferred interface, usually the physical adapter. Multicast may have similar interface-selection problems, so Phase 0 tests it separately.

Non-IP protocols are the one intentional scope difference from Hamachi, which [also carries IPX on Windows](https://bafybeiemxf5abjwjbikoz4mc3a3dla6ual3jsgpdr4cjr3oz3evfyavhwq.ipfs.4everland.io/wiki/LogMeIn_Hamachi.html). Keep the packet device abstract so a TAP backend can be added if needed.

### Local discovery modes

| Mode | How it works | Benefit | Cost |
| --- | --- | --- | --- |
| Capture (default) | The service captures local broadcasts through loopback and sends them to peers | Does not affect the physical LAN | Cannot see packets with identical source and destination ports |
| Compatibility mode | Lower the virtual adapter metric | Better for multicast and IP-in-payload protocols | Can affect physical network broadcast while active |

Capture mode uses the [WinIPBroadcast](https://github.com/dechamps/WinIPBroadcast) idea. That project is GPL-3.0, so do not copy its code; implement the method independently. Compatibility mode follows the [ZeroTier guidance](https://zerotier.atlassian.net/wiki/x/NwBz) and restores the old metric when the application exits.

Write incoming broadcast and multicast packets to Wintun with the sender's virtual IP as source. Rewrite outgoing packet sources to the virtual IP and update checksums. Because Wintun is Layer 3, verify both directions in Phase 0.

### Adapter settings

- Configure `10.77.0.x/24` and add no default route. Internet traffic does not enter the tunnel.
- Windows may block inbound traffic if the adapter is Public. Setup marks it Private and adds a firewall rule only for this interface.
- The service and adapter require administrator privileges. The UI runs as a normal user and talks to the service through a named pipe.

### Compatibility validation

**Success is measured by behavior tests, not a list of games.** `lanprobe` tests every behavior above between two machines without a game.

- Use real games only to confirm that tests reflect reality.
- Choose test samples from different behavior classes. They do not limit supported games.
- If a game fails, identify the missing network behavior and add it generally. Do not add game-specific rules.
- Anti-cheat and online account checks remain outside Lanfolk's scope.

## Security and minimal surface

**A virtual network places members on the same LAN, so system services are blocked while game ports stay open.** Game ports cannot be known in advance, so the filter is a denylist, not an allowlist.

| Risk | Mitigation |
| --- | --- |
| Access to file sharing or remote desktop | Block TCP 135, 139, 445, 3389, 5985-5986 and UDP 137-138 |
| Connecting to the wrong person | Friend codes carry public keys, and keys are verified |
| Unauthorized network changes | The owner signs member lists, and versions cannot roll back |
| Spoofed source IP | Drop packets whose source is not the sender's virtual IP |
| Removed member access | Close the tunnel and remove the key after a new list arrives |
| Relay reading content | WireGuard encrypts end to end; the relay has no keys |
| Relay abuse | Secret session IDs, session and IP limits, rate limits |
| Member relay abuse | Works only for the same network and has rate limits |
| DHT metadata leakage | Salt is secret and addresses are encrypted |
| Members seeing your real IP | Offer a **Relay only** setting; ping may increase |
| Local privilege escalation | Named pipe is limited to the session owner; commands are validated |
| Fake updates | Releases are signed and updates verify signatures |

The firewall rule applies only to the Lanfolk adapter. The tunnel provides no internet exit. VPS credentials are never written to the repository, docs or images.

## Agent implementation plan

**Execute phases in order. Do not start the next phase until the current phase gate passes.** Tasks marked `[Human]` require user access or real hardware. For those tasks, prepare the tools and instructions, collect the results from the user, then continue.

> **Progress tracking.** A ticked item is implemented and verified by automated tests or by the steps noted under it. Notes marked *Deviation* explain where the implementation differs from this plan. The full rationale for each `D-nnn` reference is in [DECISIONS.md](DECISIONS.md), and the [implementation log](#implementation-log) at the end of this section summarizes them. Gates that need real hardware or ISP lines stay open until the results are recorded (D-001).

### IMPORTANT: Server safety

**On the VPS, you may create only the new `lanfolk-relay` Docker container and resources that belong to it. Protect all other programs, data and settings. The behavior of every other application must remain unchanged.**

- Before deployment, run read-only checks only: OS, Docker version, running containers and occupied ports.
- Create only `lanfolk-relay` container, image and network settings, plus a small deployment folder at `/opt/lanfolk-relay`. Create that folder only if it does not already exist.
- Do not change existing containers, images, Docker settings, system packages, services, users, SSH settings, firewall rules or files.
- Never run `docker system prune`, `docker image prune`, bulk deletion, Docker daemon restarts or system reboots.
- If Docker is missing, a required port is occupied or a firewall blocks traffic, make no server changes. Explain the issue in Turkish and wait for the user's decision.
- Rollback may remove only `lanfolk-relay` resources. Compare the running container list before and after deployment.

### Starting inputs

| Input | Provider | Use |
| --- | --- | --- |
| GitHub repository and write access | User | Code, CI and releases |
| Local `.env` file | User | VPS `ip`, `username` and `password` |
| Two Windows test machines | User | Driver, LAN and setup tests |
| Real ISP lines | User | NAT, DHT and connection measurements |
| Code-signing certificate | User | Sign release files |

`.env` format:

```dotenv
# VPS server
ip=<vps-ip>
username=<ssh-user>
password=<ssh-password>
```

Read this file to connect to the VPS and deploy the relay. Use `ip` as `RELAY_PUBLIC_HOST`. Add `.env` to `.gitignore` before any other work. Never write real values to commits, logs, docs, test output or images.

### Agent working rules

- Speak to the user in Turkish.
- Use English for all reasoning, code, comments, commit messages, branch names, issue text, tests, logs and documentation.
- Keep the repository entirely in English, including UI strings.
- Write tests before code in every phase.
- Update `docs/PROTOCOL.md` for every protocol change.
- Do not carry failing code into the next phase. Record blockers and decisions in `docs/DECISIONS.md`.
- Always apply the IMPORTANT server safety rules.

### Phase 0: Foundation and measurement

- [x] Set up the Go module, CI and repository layout
  - *Deviation (D-002):* CI workflows exist but run only on manual dispatch. Builds and tests run locally with Go 1.27.3, MinGW-w64 GCC (for `go test -race`) and Docker.
- [ ] Build `natprobe`: external port behavior, filtering, NAT timeout, IPv6 and DHT access
- [ ] Build `lanprobe`: unicast, subnet broadcast, global broadcast and multicast
- [ ] Test broadcast and multicast send and receive through Wintun
- [ ] Verify plain WireGuard traffic can pass through test lines
- [ ] `[Human]` Run `natprobe` on Türk Telekom, Superonline, TurkNet, Vodafone and mobile lines

**Gate:** Measurements are recorded in `docs/MEASUREMENTS.md`, the LAN method is selected and transport is confirmed.

### Phase 1: Static tunnel

- [ ] Create the Wintun adapter and assign IP and MTU
- [ ] Integrate wireguard-go with a custom `conn.Bind`
- [ ] Connect two manually configured clients
- [ ] Build the overhead measurement tool

**Gate:** Virtual IP ping works and direct-path overhead meets the target.

### Phase 2: Docker relay

- [x] Implement STUN, session management and UDP and TCP forwarding in `lanfolk-relay`
  - *Deviation (D-007):* relay data frames carry a 1-byte kind plus the 16-byte session ID (17 bytes). The 1380-byte MTU budget still fits and is enforced by a test.
- [x] Add rate limits, metrics and health checks
- [x] Create the Dockerfile, `deploy/relay/docker-compose.yml` and `.env.example`
  - *Deviation (D-006):* the non-root relay binds ports below 1024 through a file capability, so `no-new-privileges` is not set.
- [x] Build and scan the image in CI
  - *Deviation (D-002):* built locally and scanned locally with Trivy instead of CI.
- [x] Read `.env` and connect to the VPS with only the required access
  - A local SSH helper (outside the repository) masks the IP, user name and password in all output and pins the host key on first use.
- [x] Run read-only checks and save the running container list
- [x] Select free ports; if 443 is occupied, use another TCP port without touching the existing service
  - TCP 443 belongs to an existing reverse proxy, so the TCP fallback uses 8443 (D-003). UDP 3478 and 127.0.0.1:9100 were free.
- [x] Create `/opt/lanfolk-relay` and start only the `lanfolk-relay` project
  - *Deviation (D-004):* UFW blocked the relay ports. Deployment stopped and the user explicitly approved exactly two rules: `3478/udp` and `8443/tcp`.
  - *Deviation (D-005):* the image is built locally and copied with `docker save` and `docker load`, so the VPS never pulls build images.
- [x] Run external STUN, UDP and TCP tests
  - Passed from a home line in Turkey: STUN RTT 77 ms; relay round trip between two sockets on the same line 147 ms (`internal/relay/external_test.go`).
- [x] Compare the container list after deployment and report the result in Turkish
  - Only `lanfolk-relay` was added; the other 40 containers are unchanged. The container is healthy again after `docker restart`.

**Gate:** Two CGNAT lines tunnel through the relay, the container returns after restart and all other VPS systems are unchanged.

> Status: restart and VPS isolation passed. The two-CGNAT-line tunnel test needs the client from Phase 3 onward.

### Phase 3: Identity and friends

- [x] Implement key generation, DPAPI storage and friend codes
  - *Deviation (D-008):* friend codes carry a 4-byte checksum so that mistyped codes are rejected.
  - *Deviation (D-009):* WireGuard static keys are derived from the Ed25519 identity key, so one friend code is enough to configure a peer.
- [ ] Implement mutual friend adding
- [x] Derive session IDs and encryption keys from the pair secret
  - *Deviation (D-010):* the BEP 44 signing key is also derived from the pair secret, so DHT records cannot be linked to identities.
- [ ] Exchange status and candidates through the relay
- [ ] Complete the end-to-end flow with `lanfolk-cli`

**Gate:** Two clients become friends by code and establish a tunnel automatically.

### Phase 4: Direct path

- [ ] Implement STUN and candidate collection
- [ ] Demultiplex packet types on one UDP socket
- [ ] Implement probes and the path selection state machine
- [ ] Add IPv6, UPnP, NAT-PMP and PCP support
- [ ] Add manual port settings
- [ ] Handle reconnection after path loss and IP changes

**Gate:** Suitable line pairs move from relay to direct path without user action.

### Phase 5: Private networks

- [ ] Implement network creation and member add and remove flows
- [x] Implement signed member lists and version checks
  - Implemented and tested in `internal/network`; service integration is tracked by the other items of this phase.
- [x] Implement virtual IP allocation
- [ ] Implement full-mesh member connections
- [ ] Test the owner-offline scenario

**Gate:** Three members connect to one private network and outsiders cannot join.

### Phase 6: Member relay and DHT fallback

- [ ] Implement the member relay protocol and rate limits
- [ ] Select relays by total RTT
- [ ] Implement BEP 44 publish and lookup
- [ ] Implement time-limited connection codes
- [ ] Test the relay-down scenario

**Gate:** With the shared relay down, a suitable member and DHT can establish a connection.

### Phase 7: LAN behavior

- [ ] Implement broadcast and multicast capture and copying
- [ ] Write incoming packets to the adapter
- [ ] Implement compatibility mode and metric restoration
- [ ] Configure Private profile and firewall rule for the adapter
- [ ] Add the denylist port filter
- [ ] Complete the `lanprobe` test suite
- [ ] `[Human]` Test real games from different LAN behavior classes across two homes

**Gate:** `lanprobe` passes every supported behavior and real games show matching results.

### Phase 8: UI and installer

- [ ] Implement the named pipe protocol between service and UI
- [ ] Build friend, private network and member screens
- [ ] Show path type, ping, jitter, loss, quality and relay details per member
- [ ] Update connection changes live
- [ ] Add settings: Relay only, compatibility mode, manual port and relay address
- [ ] Add diagnostic report export
- [ ] Build the Windows installer, service registration and uninstaller

**Gate:** On clean Windows, setup, adding friends, creating a network and uninstalling all work.

### Phase 9: Hardening

- [ ] Add NAT simulation tests to CI
- [ ] Remove data-path allocations and latency outliers
- [ ] Run security tests and protocol fuzzing
- [ ] Load test the relay
- [ ] `[Human]` Run a closed beta with at least 10 users and 4 ISPs

**Gate:** Acceptance criteria pass and no known critical bug remains.

### Phase 10: Release

- [ ] Complete license files and third-party notices
- [ ] Publish `README`, setup and relay documentation
- [ ] `[Human]` Approve code signing
- [ ] Publish v1.0 on GitHub Releases

**Gate:** A new user can use the system with only the release documentation.

### Implementation log

Deviations and decisions so far. See [DECISIONS.md](DECISIONS.md) for details.

| ID | Area | Summary |
| --- | --- | --- |
| D-001 | Process | Gates that need real hardware stay open; implementation continues with automated tests as the technical gate |
| D-002 | CI | Workflows are manual-only at the user's request; everything is built and tested locally |
| D-003 | Relay | TCP fallback on 8443 on the current VPS because 443 is taken; code default stays 443 |
| D-004 | Server safety | Two UFW rules (3478/udp, 8443/tcp) added with the user's explicit approval |
| D-005 | Server safety | Relay image built locally and loaded with `docker save`/`docker load` |
| D-006 | Relay image | File capability for low ports; no `no-new-privileges` |
| D-007 | Protocol | Relay header 17 bytes, member relay header 25 bytes; MTU 1380 still fits |
| D-008 | Identity | Friend codes include a 4-byte checksum (66 characters) |
| D-009 | Identity | WireGuard keys derived from the Ed25519 identity key |
| D-010 | Discovery | Per-pair BEP 44 signing keys |
| D-011 | Latency | High-resolution clock on Windows; `time.Now` steps are about 0.5 ms |
| D-012 | Repository | Module path `github.com/NotrinoResearch/lanfolk`; relay host injected at build time, never committed |

## Test plan

**Success is determined by measurements on real ISP lines; simulation catches regressions.**

| Test | Method | Pass criterion |
| --- | --- | --- |
| Tunnel overhead | Raw UDP vs tunneled ping on the same endpoints, 10,000 packets | At most 1 ms p50 and 3 ms p99 |
| Small-packet loss | 64 packets/s, 200 bytes, 2 hours | Added loss below 0.1% |
| ISP matrix | Türk Telekom, Superonline, TurkNet, Vodafone, mobile | Every pair connects and path type is recorded |
| CGNAT scenario | Three members behind CGNAT | Network forms without opening ports |
| NAT simulation | Linux network namespaces and nftables | All types connect; direct paths form where expected |
| Relay outage | Stop and restart the relay | Traffic fails over or returns automatically |
| LAN behaviors | `lanprobe` unicast, broadcast and multicast | Every supported behavior passes |
| Real-game validation | 2-hour sessions across different LAN behavior classes | Discovery, connection and disconnects are recorded |
| Network impairment | `tc netem` delay, loss and reordering | Tunnel does not lock up and connections recover |
| Path change | Wi-Fi to wired switch, router restart | Connection recovers automatically |
| Security | Outsider tunnel attempts and blocked-port scans | Packets drop; SMB and RDP remain closed |
| Installer | Clean Windows 10 and 11 VMs | Install and uninstall leave no residue |

Compare in-game latency with measured raw path latency. Hamachi or ZeroTier can be used as benchmarks on the same lines.

## Risks and scope decisions

**The largest risks are relay latency from Germany, Wintun LAN behavior and ISP filtering.** Measure critical results before building on them.

| Risk | Impact | Mitigation |
| --- | --- | --- |
| All members behind hard NAT | Traffic uses the German relay | Measure relay ping; add a closer relay if needed |
| Single relay outage | Final fallback path is lost | Automatic restart, health checks, member relay and DHT fallback |
| Wintun does not carry broadcast or multicast | Discovery-based games do not appear | Phase 0 test, compatibility mode, TAP backend if needed |
| ISP filtering | Interruptions or added loss | TCP fallback and replaceable transport |
| DHT is blocked | Fallback discovery fails | Relay remains primary; time-limited connection code remains available |
| Antivirus or SmartScreen warnings | Lower install trust | Code signing and open-source builds |
| Game announces the wrong IP | Peer is visible but cannot connect | Compatibility mode and diagnostic report |
| Relay abuse | Cost and IP blocks | Secret session IDs and IP and rate limits |

Most DPI information for Turkey comes from VPN vendors. Do not treat it as confirmed without independent measurement. Choose any obfuscation based on latency impact.

### Scope decisions

- The project name is Lanfolk.
- The repository language is English. The agent speaks with the user in Turkish.
- The first relay runs in a new Docker container on the user's VPS in Germany.
- The shared relay is the final path and fast signaling channel. DHT is a fallback.
- Non-IP protocols are outside the first release.
- A user is active in one private network at a time.
- The implementation language is Go.

## Open-source release

**Use Apache-2.0 because it is permissive and includes an explicit patent grant.** This is not legal advice. Verify third-party terms file by file before release.

| Component | Status | Action |
| --- | --- | --- |
| [wireguard-go](https://blog.cloudflare.com/boringtun-userspace-wireguard-rust) | MIT | Include license notice |
| [Wintun](https://www.wintun.net/) | Source GPL-2.0; signed DLL has a separate license | Distribute only signed DLLs and include the DLL license |
| [anacrolix/dht](https://pkg.go.dev/github.com/anacrolix/dht/v2) | MPL-2.0 | Follow file-level license terms |
| [WinIPBroadcast](https://github.com/dechamps/WinIPBroadcast) | GPL-3.0 | Do not copy code; implement independently |
| WireGuard name | Registered trademark | Do not use in the product name; state compatibility only |
| Hamachi | Closed-source product | Do not copy code or protocols |

### Release package

- `README` with screenshots and quick start
- `docs/PROTOCOL.md`: Packet formats, address exchange and relay protocol
- `docs/SELF_HOST.md`: Relay deployment
- `SECURITY.md`: Security reporting process
- `CONTRIBUTING.md` and code of conduct
- Informational compatibility table that does not limit support

Sign the Windows installer and `exe` files. Research free code-signing programs for open-source projects before release.

## Sources

- [BEP 44](https://bittorrent.org/beps/bep_0044.html): DHT mutable items, salt, signatures and the 1000-byte limit
- [pkarr](https://github.com/pubky/pkarr): Using Mainline DHT for public-key discovery
- [anacrolix/dht](https://pkg.go.dev/github.com/anacrolix/dht/v2): Go DHT library
- [Tailscale Peer Relays](https://tailscale.com/blog/peer-relays-beta): Relaying through network nodes
- [Wintun](https://www.wintun.net/): Layer 3 driver, signed DLL and license
- [Microsoft driver signing requirements](https://learn.microsoft.com/pl-pl/windows-hardware/drivers/dashboard/code-signing-reqs): EV certificates and attestation
- [WinIPBroadcast](https://github.com/dechamps/WinIPBroadcast): Windows broadcast behavior and capture method
- [ZeroTier LAN broadcast guidance](https://zerotier.atlassian.net/wiki/x/NwBz): Metric workaround and side effect
- [Pinggy: How does NAT traversal work?](https://pinggy.io/blog/how_nat_traversal_works/): NAT types, hole punching, ICE and CGNAT
- [Cloudflare: BoringTun](https://blog.cloudflare.com/boringtun-userspace-wireguard-rust): wireguard-go and BoringTun comparison
- [Fexyn: VPN for Turkey](https://www.fexyn.com/vpn-for-turkey): DPI claims for Turkey; vendor source, requires verification
