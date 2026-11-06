# Lanfolk protocol

**Protocol version 1.**

This document specifies every byte that Lanfolk puts on the wire, every
key it derives, and the local control protocol between the service and its
clients. It is written so that an independent implementation can interoperate.
Packet formats are defined by the code in `internal/wire`, `internal/stun`,
`internal/peermsg`, `internal/network`, `internal/discovery`,
`internal/identity` and `internal/ipc`. Where this document and the code
disagree, the code is the reference and this document has a bug: please open
an issue.

Every protocol change must update this file and add an entry to the
[change log](#change-log).

## Contents

1. [Conventions](#1-conventions)
2. [Overview and single-socket demultiplexing](#2-overview-and-single-socket-demultiplexing)
3. [Identity, friend codes and key derivation](#3-identity-friend-codes-and-key-derivation)
4. [Shared relay protocol](#4-shared-relay-protocol)
5. [Control frames and peer messages](#5-control-frames-and-peer-messages)
6. [Path probing and selection](#6-path-probing-and-selection)
7. [Member relay](#7-member-relay)
8. [LanFrame](#8-lanframe)
9. [Signed member list](#9-signed-member-list)
10. [DHT record, connection code and invite code](#10-dht-record-connection-code-and-invite-code)
11. [Local control protocol](#11-local-control-protocol)
12. [Change log](#12-change-log)

## 1. Conventions

- All integers are unsigned and big endian ("network order") unless stated
  otherwise. `u8`, `u16`, `u32` and `u64` are 1, 2, 4 and 8 bytes.
- `a | b` is concatenation. `[n]` after a name is a fixed length in bytes.
- Text fields are UTF-8, preceded by a one-byte length, and never
  NUL-terminated. A receiver rejects text that is not valid UTF-8 or is longer
  than the field limit.
- A "datagram" is one UDP payload. The largest datagram Lanfolk sends or
  accepts is **1600 bytes** (`wire.MaxPacket`). Longer datagrams are dropped.
- The virtual network is `10.77.0.0/24`, the virtual MTU is **1380**, and the
  adapter is named `Lanfolk`.
- Parts marked **(service)** are implemented in `internal/engine` rather
  than in a reusable package. The numbers come from the design (see
  [DECISIONS.md](DECISIONS.md)) and may still be tuned by measurements.
  Constants that are configurable in code are given with their defaults.

## 2. Overview and single-socket demultiplexing

A Lanfolk client owns **one UDP socket**. STUN, path probes, shared relay
traffic, peer control messages, member relay traffic and WireGuard all use it.
The hole punched for one purpose is therefore the same mapping the tunnel
uses. The first byte of each datagram selects the protocol:

| First byte | Protocol | Rule |
| --- | --- | --- |
| `0x00`-`0x3F` | STUN (RFC 5389) | Bytes 4-7 equal the magic cookie `0x2112A442`, bits 7-6 are zero, and the 16-bit message length plus 20 fits in the datagram |
| `0x01`-`0x04` | WireGuard | Bytes 1-3 are zero and the size matches the message type (below) |
| `0xF0` | Shared relay control | At least 3 bytes |
| `0xF1` | Shared relay data | At least 17 bytes |
| `0xF2` | Peer control frame | At least 57 bytes |
| `0xF3` | Member relay, towards the relaying member | At least 25 bytes |
| `0xF4` | Member relay, towards the receiver | At least 25 bytes |

WireGuard message types and sizes (the standard WireGuard protocol,
unmodified):

| First byte | Message | Size |
| --- | --- | --- |
| `0x01` | Handshake initiation | exactly 148 bytes |
| `0x02` | Handshake response | exactly 92 bytes |
| `0x03` | Cookie reply | exactly 64 bytes |
| `0x04` | Transport data | at least 32 bytes |

A datagram that matches none of these rows is dropped. The BitTorrent DHT
(section 10) uses a separate socket and is not demultiplexed here.

Notes on the STUN row:

- STUN Binding requests (message type `0x0001`) start with byte `0x00`.
  Binding success responses (message type `0x0101`) start with byte `0x01`,
  which is also the first byte of a WireGuard initiation. The reference
  classifier `wire.Classify` checks WireGuard first and returns "unknown" for a
  `0x01`-prefixed datagram that is not a 148-byte WireGuard initiation, so
  it does not classify STUN success responses. Code that needs them
  parses them with `stun.ParseBindingResponse` on a socket or code path that
  expects a reply (as `natprobe` does).
- The shared relay answers STUN Binding requests only. It does not implement
  other STUN features: no authentication, no attributes other than
  `XOR-MAPPED-ADDRESS` in responses, no `SOFTWARE`, no `FINGERPRINT`.

### Transport carriers

The same frames travel on different carriers:

| Carrier | Used for | Frame encoding |
| --- | --- | --- |
| UDP, direct to the peer | WireGuard, control frames | the datagram is the frame |
| UDP, to the shared relay | Relay control, relay data | the datagram is the frame |
| TCP, to the shared relay | Same frames, when UDP does not work | 16-bit length prefix, see [4.4](#44-tcp-framing) |
| Inside relay data (`0xF1`) | A peer datagram (WireGuard or control) relayed by the shared relay | payload of the relay data frame |
| Inside member relay (`0xF3`/`0xF4`) | A peer datagram relayed by another member | payload of the member relay frame |

A datagram carried inside a relay data frame or a member relay frame is
classified again by its own first byte, exactly as if it had arrived directly.

## 3. Identity, friend codes and key derivation

### 3.1 Identity

A user has one **Ed25519** key pair generated on first start. The 32-byte
public key is the user's identifier. There is no registry and no account.

On Windows the 32-byte seed is stored encrypted with DPAPI (bound to the
account that runs the service, which is LocalSystem for the installed
service, with the entropy string `lanfolk-identity-v1`); the
stored blob starts with the ASCII text `dpapi:`. An exported identity backup is
`lanfolk-backup1:` followed by the unpadded URL-safe base64 of
`salt[16] | nonce[24] | ciphertext`, where the key is
Argon2id(password, salt, time = 3, memory = 64 MiB, threads = 4, length = 32)
and the AEAD is XChaCha20-Poly1305 with the prefix `lanfolk-backup1:` as
associated data. The plaintext is `seed[32] | nickname (UTF-8, at most 32 bytes)`.

Nicknames are display text only, at most 32 bytes of UTF-8.

### 3.2 Key ID

A **key ID** is the first 8 bytes of `SHA-256("lanfolk/v1/keyid" | public_key)`.
It is used where a full key would waste bytes (member relay headers,
connection codes, relay offers).

### 3.3 Friend codes

A friend code is the public key in text form:

```
code     = "lanfolk1" | z-base32(public_key[32] | checksum[4])
checksum = first 4 bytes of SHA-256("lanfolk1" | public_key[32])
```

- z-base32 uses the alphabet `ybndrfg8ejkmcpqxot1uwisza345h769`, the usual
  RFC 4648 bit order (first character holds the first five bits) and no
  padding. 36 bytes encode to 58 characters, so a code is **66 characters**
  long.
- When parsing, spaces, tabs, line breaks and `-` are removed and letters are
  lowercased first. The prefix must be `lanfolk1`, the decoded length must be
  exactly 36 bytes, the checksum must match, and the key must be a valid
  Ed25519 point. Anything else is rejected (decision D-008).
- A friendship becomes active only after both sides have entered each other's
  code. A one-sided entry grants nothing.

### 3.4 Key agreement and derived secrets

Ed25519 keys are converted to X25519 (decision D-009), the same way libsodium
does it:

- Private: `SHA-512(seed)[0:32]` with the X25519 clamping applied
  (`s[0] &= 248; s[31] &= 127; s[31] |= 64`).
- Public: the Montgomery form of the Ed25519 public key.

The converted keys are used both as WireGuard static keys and for the pair
secret, so a friend code alone is enough to configure a WireGuard peer. The
protocol derives no WireGuard pre-shared key.

The **pair secret** of two users is

```
shared = X25519(local_x25519_private, remote_x25519_public)
secret = HKDF-SHA256(ikm = shared,
                     salt = "lanfolk/v1/pair",
                     info = lower_key | higher_key,   // both Ed25519 public keys, sorted bytewise
                     length = 32)
```

Both sides compute the same value. A user cannot pair with their own key.

Every other value is `HKDF-Expand-SHA256(prk = secret, info = label | context, length)`,
where `label` is the ASCII string below and `context` is the bytes listed:

| Value | Label | Context | Length |
| --- | --- | --- | --- |
| Relay and control session ID | `lanfolk/v1/relay-session` | none | 16 |
| Control key of sender X | `lanfolk/v1/control` | X's public key (32) | 32 |
| DHT salt of publisher X | `lanfolk/v1/dht-salt` | X's public key | 16 |
| DHT signing seed of publisher X | `lanfolk/v1/dht-sign` | X's public key | 32 (Ed25519 seed) |
| DHT encryption key of publisher X | `lanfolk/v1/dht-enc` | X's public key | 32 |
| DHT epoch secret | `lanfolk/v1/dht-epoch` | epoch as `u32` | 32 |
| Connection code key of sender X | `lanfolk/v1/conncode` | X's public key | 32 |

- `ControlSendKey` is the control key with X = the local key; `ControlRecvKey`
  is the control key with X = the remote key. A message is always encrypted
  with its sender's key.
- The `dht-epoch` value replaces `secret` for all `dht-*` derivations of that
  day; see [10.1](#101-dht-record). The epoch is `floor(unix_seconds / 86400)`
  (the UTC day number).
- A **session ID** identifies the pair at the shared relay and in control
  frames. Only the two peers can compute it, which is why the relay cannot
  link it to identities.

## 4. Shared relay protocol

The relay is a small forwarding service. It never decrypts anything and keeps
no data on disk. It does three jobs: it answers STUN Binding requests, it
tells a client the public address it sees, and it forwards frames between the
two parties registered under the same session ID.

### 4.1 Relay control messages (`0xF0`)

```
0xF0 | version[1] = 1 | type[1] | body
```

A frame with another version is answered with a `Reject` carrying code 6 and
an all-zero session ID. A frame with an unknown type, or a body shorter than
the table says, is dropped. A client sends control messages on the same UDP
socket it uses for everything else.

| Type | Name | Direction | Body |
| --- | --- | --- | --- |
| `0x01` | Register | client to relay | `session[16] \| token[8] \| [cookie[16]]` |
| `0x02` | RegisterAck | relay to client | `session[16] \| peer_present[1] \| observed address` |
| `0x04` | Ping | client to relay | `nonce[8]` |
| `0x05` | Pong | relay to client | `nonce[8] \| observed address` |
| `0x06` | Reject | relay to client | `session[16] \| code[1]` |
| `0x07` | Unregister | client to relay | `session[16] \| token[8]` |
| `0x08` | PeerJoined | relay to client | `session[16]` |
| `0x09` | EchoRequest | client to relay | `nonce[8] \| delay_seconds[1]` |
| `0x0A` | EchoReply | relay to client | `nonce[8] \| observed address` |
| `0x0B` | Challenge | relay to client | `session[16] \| cookie[16]` |

Type `0x03` is unused. The cookie of Register is optional: a client appends
it only to answer a Challenge (section 4.3.1). Relays before release 0.4
ignore it, as they ignore any bytes after a known body. "Observed address" uses the
[address encoding](#address-encoding) and is the source address the relay saw
on the frame it is answering (after unmapping IPv4-in-IPv6). `peer_present` is
`1` if the other party of the session is registered, `0` otherwise.

Reject codes:

| Code | Name | Sent by the relay | Meaning |
| --- | --- | --- | --- |
| 1 | BadRequest | no (reserved) | Malformed request |
| 2 | SessionLimit | yes | This source IP already holds the maximum number of sessions |
| 3 | SessionFull | yes | Both slots are taken by active parties with other tokens |
| 4 | RateLimited | no (reserved) | Over the rate limit |
| 5 | Busy | yes | The relay holds its maximum number of sessions, or too many delayed echoes are pending |
| 6 | VersionUnsupported | yes | Relay control version is not 1 |

Rate-limited frames are dropped without an answer, so a client sees silence,
not code 4.

### 4.2 Relay data (`0xF1`)

```
0xF1 | session[16] | payload
```

The relay checks that the sender is a registered party of `session` (same
source address as registered), and forwards **the whole frame unchanged** to
the other party. The payload is one peer datagram (WireGuard or a control
frame). The header costs 17 bytes. Frames are dropped, and counted in the
relay metrics, when the sender is not registered, when no second party is
registered, or when the session's rate limit is exceeded.

### 4.3 Session semantics

A session has at most two **parties**. A party is identified by the 8-byte
random `token` its client generated for the lifetime of the client process, and
located by its source endpoint (UDP address or TCP connection).

On `Register(session, token)`:

1. **Unknown session.** The relay creates it with this party. It answers
   `RegisterAck(peer_present = 0)`. It rejects with `Busy` if it already holds
   the maximum number of sessions, and with `SessionLimit` if the source IP
   already holds the maximum number of parties.
2. **Known token.** This is a refresh or **roaming**: if the source endpoint
   changed, the party moves to the new endpoint (a changed source IP counts
   against the per-IP limit). The relay answers
   `RegisterAck(peer_present = whether the other party exists)`. A refresh
   marks the party as active. If the other slot holds a party at the new
   endpoint, that party is removed (see case 3).
3. **Unknown token from the endpoint of a party (takeover).** The client
   restarted on the same socket without its `Unregister` reaching the relay
   (killed, crashed, power cut), so the party's earlier run is gone: the
   party takes the new token and stays in its slot. The relay answers
   `RegisterAck(peer_present = whether the other party exists)` and sends
   `PeerJoined(session)` to the other party, which greets the new run at
   once. Two parties of one session never share an endpoint (D-041).
4. **Unknown token, free slot.** The party joins. The relay answers
   `RegisterAck(peer_present = 1)` and sends `PeerJoined(session)` to the
   other party.
5. **Unknown token, session full.** The new party takes the slot of a party
   that has been silent (no `Register`, no relay data) for more than **a
   quarter of the idle timeout, 30 s by default**. Otherwise the relay
   answers `Reject(SessionFull)`.

Relay data is attributed to a party by its source endpoint. Without case 3
an earlier run's party at the same address was kept fresh by the new run's
own data, so the other side met `SessionFull` for as long as the client ran,
and the new run's frames were relayed back to itself (D-041).

#### 4.3.1 Register cookie (under load)

A Register over UDP proves nothing about its source address, so a flood of
Registers with forged source addresses could fill the session table. While
the relay is **under pressure**, a UDP Register that would create a new
session (case 1) or a new party (cases 3 to 5) must carry a cookie:

1. Without a valid cookie the relay creates no state and answers
   `Challenge(session, cookie)`.
2. The client repeats the Register at once with `cookie` appended (at most
   once per session and second). It keeps sending the cookie with its
   refreshes for 25 s.
3. The relay accepts the cookie if it was made for the same source address
   and port and the same session in the current or the previous 30-second
   window, so a cookie is valid for 30 to 60 s.

The cookie is the first 16 bytes of
`SHA-256(secret[32] | address as 16 bytes (IPv4 mapped) | port[2] | session[16] | window[8])`,
where `secret` is random per relay process and `window = floor(unix_seconds / 30)`.
Only the relay ever checks it, so its construction is not part of the
interoperability contract.

A refresh or roaming of a known token (case 2) never needs a cookie, and
neither does a Register over TCP: the TCP handshake has already proven the
address. The relay is under pressure while the session table is at least
`RELAY_CHALLENGE_SESSIONS_PCT` percent full (default 50) or more than
`RELAY_CHALLENGE_NEW_PER_SEC` new sessions or parties are requested over UDP
in one second (default 1000), and for 30 s after the last time either was
true. `RELAY_CHALLENGE` can force the cookie on (`always`) or off (`off`).

Clients before release 0.4 drop Challenge as an unknown message, so they
cannot create new sessions over UDP while a relay is under pressure (existing
registrations keep working, and the TCP fallback is not affected). This is
intended: during an attack the relay keeps serving the clients that can prove
their address.

`Unregister(session, token)` removes the party only if the token **and** the
source endpoint match. It is never answered. A session disappears when its
last party is removed.

**Idle timeout.** A party that sends neither `Register` nor relay data for
the idle timeout (**120 s** by default) is removed. `Ping` and STUN do not keep
a party alive. A client therefore re-registers every 30 s. A TCP connection that
stays silent for the idle timeout is closed, which removes its parties.

**Ping and observed address.** `Ping(nonce)` is answered with
`Pong(nonce, observed)`. The observed address is how a client learns its
public UDP address without a separate STUN server.

**Delayed echo.** `EchoRequest(nonce, delay)` makes the relay answer with
`EchoReply(nonce, observed)` to the same endpoint after `delay` seconds
(0-255, capped by the relay, **180 s** by default). `natprobe` uses it to
measure how long a NAT keeps a mapping open: if the reply arrives, the mapping
survived `delay` seconds of silence. At most **4096** echoes are pending, and
at most **8** per source IP; beyond that the relay answers `Reject(Busy)`.

### 4.4 TCP framing

When UDP does not work, a client opens a TCP connection to the relay's TCP
port and sends the same relay control and relay data frames, each prefixed
with its length:

```
length[2] | frame
```

`length` is 1-2048. A zero or larger length ends the connection. The relay
answers on the same connection in the same framing. A TCP-connected party is
located by its connection; when the connection closes, its parties are removed.
Relay data sent over TCP is forwarded to the other party over whichever
carrier that party registered with (the relay converts between UDP and TCP).
Each connection has an outgoing queue of 128 frames; when it is full, frames
are dropped instead of delayed, and writes time out after 5 s. TCP is used
with `TCP_NODELAY`.

### 4.5 Relay limits (defaults)

| Limit | Default | Setting |
| --- | --- | --- |
| Per-session data rate (both directions share one bucket, counts whole frames, burst of one second) | 2000 kbit/s | `RELAY_MAX_SESSION_KBPS` |
| Relay data sent by one source IP over all its sessions (burst of one second, `0` = off) | 20000 kbit/s | `RELAY_MAX_IP_KBPS` |
| All forwarded relay data (burst of one second, `0` = off) | 400 Mbit/s | `RELAY_MAX_TOTAL_MBPS` |
| Parties per source IP | 600 | `RELAY_MAX_SESSIONS_PER_IP` |
| Sessions in total | 100000 | `RELAY_MAX_SESSIONS` |
| TCP connections per source IP | 16 (extra connections are closed at once) | none |
| TCP connections in total | 2048 (extra connections are closed at once) | `RELAY_MAX_TCP_CONNS` |
| TCP connection without a session, silent for | 15 s, then closed | `RELAY_TCP_GRACE_SEC` |
| Idle timeout | 120 s | `RELAY_IDLE_TIMEOUT_SEC` |
| Control and STUN messages per source IP | 200 per second, burst 600 | none |
| Maximum delayed echo | 180 s | none |
| Pending delayed echoes | 4096 in total, 8 per source IP | `RELAY_MAX_ECHOES_PER_IP` |
| Register cookie (section 4.3.1) | under pressure only | `RELAY_CHALLENGE`, `RELAY_CHALLENGE_SESSIONS_PCT`, `RELAY_CHALLENGE_NEW_PER_SEC` |
| Largest datagram | 1600 bytes | none |

"Per source IP" means per IPv4 address and per IPv6 /64 prefix, because an
IPv6 host usually controls a whole /64. The per-IP limits on control
messages and relay data use a fixed table of buckets indexed by a keyed hash
of the address: memory does not grow with the number of sources, and a flood
from many addresses cannot lock out others. Two addresses that share a
bucket share its budget. Relay data is checked against the per-IP limit
first, then the session limit, then the total limit, so a flooding source
does not use up the budget of others.

### 4.6 Client behavior (service)

These values are used by the relay client in `internal/transport`:

- Ping every **5 s**. A transport is considered down after **12 s** without a
  Pong.
- At startup, if UDP gets no answer for **3 s**, the client opens the TCP
  connection (5 s dial timeout) and registers over it.
- While on TCP, it probes UDP again every **15 s** and switches back when
  UDP answers.
- It re-sends `Register` for every active session every **30 s**, and again
  after every change of transport.
- It resolves the relay name again every 5 minutes.
- On a clean exit it sends `Unregister` for every session before anything
  slow (closing the adapter), so that a setup that ends the service after
  5 s does not leave registrations behind.
- After `Reject(SessionFull)` it treats the peer as absent at that relay and
  registers again after **10 s** instead of 30 s. Diagnostics list the relays
  that refuse a peer's session.
  Invite sessions ([10.3](#103-invite-code)) are registered like pair sessions.
- A client registers at up to three relays at once (the default relay, up to
  three relays from the signed member list, relays from the user's settings;
  decision D-015). The relay index is the position in that list.

## 5. Control frames and peer messages

Peers talk to each other with **control frames**. They are encrypted with keys
only the two peers know, travel over any carrier of section 2, and carry the
messages listed in 5.3.

### 5.1 Control frame format (`0xF2`)

```
0xF2 | session[16] | nonce[24] | ciphertext | tag[16]
```

- Cipher: XChaCha20-Poly1305.
- Key: the sender's control key (section 3.4). The sender uses
  `ControlSendKey`; the receiver uses `ControlRecvKey` of the same pair.
- Nonce: 24 random bytes per frame.
- Associated data: the first 17 bytes of the frame (`0xF2 | session`).
- `session` is the pair's session ID; the receiver uses it to find the pair
  and so the key.
- The frame overhead is 57 bytes; the plaintext is at most 1200 bytes.

Plaintext:

```
boot[8] | counter[8] | type[1] | body
```

### 5.2 Replay protection (service)

- `boot` is chosen when the sender starts and must be larger than the value of
  every earlier run: the engine uses the Unix time in nanoseconds with the low
  16 bits replaced by random bits. `counter` is 1 for the first frame of a boot
  and increases by 1 for each frame the sender emits to that peer, on all
  carriers together. Counters of 2^62 or more are rejected.
- The receiver keeps, per peer, the highest `boot` seen and the time of the last
  accepted frame. It accepts a **new boot value only if it is larger than the
  last one, or the last accepted frame is older than 30 s**. (The second rule
  lets a peer recover after losing its state or after a clock reset.)
- For the current boot it uses a **sliding window of 8128 counters** (the
  replay filter of wireguard-go, RFC 6479). A counter outside the window or already seen
  is dropped.
- A frame that fails authentication is dropped silently.

### 5.3 Message types

| Type | Name | Purpose |
| --- | --- | --- |
| 1 | Hello | Announce presence, protocol version, nickname, capabilities and known member lists |
| 2 | MembershipUpdate | One fragment of a signed member list |
| 3 | Candidates | Current candidate addresses of the sender |
| 4 | PathProbe | Test a path and measure RTT |
| 5 | PathProbeAck | Answer a probe on the same path |
| 6 | RelayOffer | Offer to relay for other members |
| 7 | Bye | The sender is going offline |
| 8 | JoinRequest | A joiner asks the owner of an invite code for a place in a network (invite sessions only, [10.3](#103-invite-code)) |
| 9 | JoinAccepted | The owner's answer to a JoinRequest (invite sessions only) |

An unknown type, or a body that is truncated or breaks a limit, makes the
whole message invalid and it is dropped. Trailing bytes after a known body are
ignored.

Limits: `MaxCandidates` 16, `MaxOfferEntries` 90, `MaxNetRefs` 32,
`FragmentSize` 1100, plaintext 1200.

#### Hello (1)

```
protocol_version[1] | flags[1] | nickname (len[1] + UTF-8, <= 32) |
capabilities[4] | active_network[16] | ref_count[1] | ref_count * (network_id[16] | version[8]) |
[app_version (len[1] + UTF-8, <= 32)]
```

- `protocol_version` is 1.
- `flags` bit 0: **relay only**, the sender will not use direct paths.
  Bit 1: **friend**, the sender has added the receiver as a friend. A
  friendship becomes active only after a Hello with this bit arrives, so a
  co-member who never added you cannot activate it.
- `capabilities` bit 0: willing to relay for other members (member relay).
  Bit 1: understands LanFrames. Other bits are reserved and must be ignored.
- `active_network` is the sender's active network, or 16 zero bytes.
- The list names the networks the sender holds that include the receiver,
  with the member list version it has of each (at most 32). A receiver that
  holds a newer version sends a MembershipUpdate. A receiver that holds an
  older version, or does not know the network, answers with a Hello that lists
  that network with its own version (0 when unknown), which asks the sender
  for its list. A brand-new network is accepted only when its owner is an
  active friend and the receiver is a member.
- `app_version` (since release 0.3.0, optional) is the sender's Lanfolk
  release, for example `0.3.0`, so members can see who should update.
  Receivers ignore bytes after the fields they know, so a 0.2 release reads
  a Hello with this field and a 0.3 release reads one without it (version
  unknown, older than 0.3.0).

Sent every **25 s** (service). A peer is
**online** while a Hello or any other authenticated frame from it arrived in
the last **60 s**.

#### MembershipUpdate (2)

```
msg_id[4] | index[1] | total[1] | length[2] | data (<= 1100)
```

A signed member list (section 9) is cut into fragments of at most 1100 bytes
(`total` fragments, `index` from 0). The receiver reassembles by `msg_id`. A
reassembler keeps at most 4 incomplete messages per peer, drops incomplete
messages after a timeout, rejects `total` of 0 or fragments with
`index >= total`, and limits a reassembled list to 64 KiB (so `total` times
1100 must not exceed 65536). The reassembled bytes are decoded and verified as
a member list (section 9); an invalid list is discarded.

#### Candidates (3)

```
generation[4] | count[1] | count * (source[1] | address)
```

At most 16 entries. `generation` increases each time the sender re-collects its
candidates. `source` tells where the address came from:

| Value | Source |
| --- | --- |
| 1 | Local interface address |
| 2 | Public IPv6 address |
| 3 | STUN (public address seen by the relay or a STUN server) |
| 4 | Port mapping (UPnP, NAT-PMP, PCP) |
| 5 | Manual port |
| 6 | Peer reflexive (learned from an incoming probe) |
| 7 | Cache (last known good address) |
| 8 | Connection code |
| 9 | DHT |
| 10 | Learned through another member |

A receiver must accept any `source` value, including values it does not know.

#### PathProbe (4) and PathProbeAck (5)

```
PathProbe:     probe_id[8] | path_id[4]
PathProbeAck:  probe_id[8] | path_id[4] | observed address
```

The sender chooses `probe_id` (unique per probe) and `path_id` (its own
identifier for the path being probed). The receiver answers **on the same
path** with the same two values and `observed`, the source address the probe
arrived from (the address encoding with family 0 for relayed paths). The sender
measures the RTT from its own send and receive times, and learns its public
address from `observed` for direct paths.

#### RelayOffer (6)

```
count[1] | count * (peer_key_id[8] | rtt_microseconds[4])
```

At most 90 entries. See section 7.

#### Bye (7)

Empty body. The receiver marks the peer offline immediately.

#### JoinRequest (8) and JoinAccepted (9)

These two messages travel only in an invite session, sealed with the invite
key, never in a pair session. See [10.3](#103-invite-code) for the rules.

```
JoinRequest:  network_id[16] | joiner_key[32] | timestamp[8, unix seconds] |
              nickname (len[1] + UTF-8, <= 32) | signature[64]
JoinAccepted: network_id[16] | owner_nickname (len[1] + UTF-8, <= 32) |
              network_name (len[1] + UTF-8, <= 64) | signature[64]
```

The signatures are Ed25519; what is signed is defined in 10.3. In an invite
session the header's `boot` is the sender's boot value and `counter` a
per-process counter, but receivers keep no replay window: a request is
idempotent and bounded by its timestamp instead.

## 6. Path probing and selection

Every peer pair keeps a set of **paths**. The path manager (`internal/pathmgr`)
is a pure state machine; the service sends the probes it asks for and feeds
back the answers.

### 6.1 Path kinds

| Kind | UI text | Carrier |
| --- | --- | --- |
| Direct | Direct | UDP between the two peers' candidate addresses |
| Member relay | Member relay | Section 7 |
| Shared relay | Shared relay | Relay data frames over UDP, one path per relay |
| TCP fallback | TCP fallback | Relay data frames over TCP, one path per relay |

At most **24** direct paths are kept per peer; when the limit is reached, the
path that never worked, or else the oldest, is dropped.

### 6.2 Probing

Probes are PathProbe messages. Timing (`pathmgr.DefaultConfig`):

| Rule | Value |
| --- | --- |
| Probe interval for a new or restarted path (burst) | every 250 ms for 3 s |
| Probe interval on the active path | every 500 ms |
| Probe interval on a working standby path | every 5 s |
| Active path declared failed after silence of | 2 s |
| Standby path declared failed after silence of | 16 s |
| First retry of a failed candidate | 30 s |
| Retry delay | doubles each failure, at most 5 min; reset when the path works |
| A path that just failed | gets a new burst at once |
| Member relay offer expires after | 20 s |

A path is **alive** when an ack (or any authenticated frame on that path) was
received within the failure time above. Any authenticated inbound frame on a
path that has an RTT estimate refreshes its liveness. A shared relay path is
alive and probeable only while the peer is registered at that relay. If a local
network change happens, every path starts a new burst. In relay-only mode,
direct paths are never probed or used.

The smoothed RTT of a path is `srtt += (rtt - srtt) / 4`, starting at the first
sample.

### 6.3 Selection

The manager considers the paths that are alive and have an RTT sample, and
computes an **effective RTT**: the smoothed RTT, plus **10 ms** for the TCP
fallback.

1. Take the best (lowest) direct path and the best relay path (member relay,
   shared relay, TCP fallback).
2. Use the direct path unless a relay path is at least **5 ms faster**
   (`relay + 5 ms <= direct`).
3. If no path has been measured yet, use the shared relay at which the peer
   is registered (lowest relay index) so that traffic flows while probes run.
4. To avoid flapping, a switch between two paths of the same class (both
   direct, or both relays) happens only if the new path is more than **2 ms**
   better. Switching between a direct path and a relay path is not damped.
5. If nothing is usable, keep sending on the last active path.

The tunnel's WireGuard endpoint for a peer is the string
`lanfolk-peer:<64 hex characters of the peer's Ed25519 public key>`. It names
the peer, not an address, and the path manager picks the address at send time.
A switch therefore needs no WireGuard roaming.

### 6.4 Statistics and quality

For the active path, over the last **60 s**:

- **Latest ping:** the last RTT sample.
- **Average ping:** mean RTT of the answered probes.
- **Jitter:** mean absolute difference between consecutive RTT samples.
- **Loss:** unanswered probes divided by probes sent, counting only probes
  older than 1 s.

Quality uses the average ping, the jitter and the loss:

| Quality | Condition |
| --- | --- |
| Good | ping <= 60 ms and jitter <= 10 ms and loss <= 0.5 % |
| Poor | ping > 120 ms or jitter > 30 ms or loss > 2 % |
| Fair | everything else |
| Unknown | no samples |

The thresholds are configuration values (`pathmgr.Thresholds`).

### 6.5 Keepalive

The active path is probed every 500 ms, which keeps NAT mappings open far
inside the 15 s limit the design asks for. Standby paths are probed every 5 s.
The relay client pings the relay every 5 s.

## 7. Member relay

When two members cannot reach each other directly, a third member that can
reach both may relay between them. Only members of the same active network
relay for each other, and only a member that announced capability bit 0 in its
Hello is willing to relay.

### 7.1 Frames

```
0xF3 | session[16] | target_key_id[8] | payload      sender -> relaying member
0xF4 | session[16] | source_key_id[8] | payload      relaying member -> receiver
```

- For `0xF3`, `session` is the session ID of the pair (sender, relaying
  member); `target_key_id` is the key ID of the final receiver. The payload is
  one complete peer datagram for the final receiver (WireGuard or a control
  frame).
- For `0xF4`, `session` is the session ID of the pair (relaying member,
  receiver); `source_key_id` is the key ID of the original sender.
- The header costs 25 bytes.

The relaying member does not decrypt the payload. It forwards only between
members of its active network that it reaches **directly**, and never
forwards a frame to a member it only reaches through a relay. The sender and
receiver authenticate the inner datagram with their own keys, so a relaying
member cannot read or forge it.

### 7.2 Limits and offers (service)

- A relaying member limits forwarding to **2000 kbit/s per source member**.
- It broadcasts a **RelayOffer** to its directly reachable members **every 5 s**,
  listing the members it reaches directly and its RTT to each.
- A receiving member treats the sender as a candidate relay for each listed
  peer, with the advertised RTT. An offer **expires after 20 s** unless
  renewed.
- The total RTT of a member relay path is measured by probes sent through it
  (section 6), not taken from the offer. The offer only creates the path.

## 8. LanFrame

Broadcast and multicast are not carried by WireGuard (it routes unicast IP by
destination), so Lanfolk wraps them. A LanFrame is an ordinary IPv4 packet
written into the WireGuard tunnel towards one member's virtual IP:

```
IPv4 header (20 bytes) | 'L' 'F' 0x01 0x00 | original IPv4 packet
```

- Outer IPv4 header: version 4, header length 5 words (no options), protocol
  **253** (experimentation protocol number), flags DF, TTL 64, source = the
  sender's virtual IP, destination = the receiving member's virtual IP, valid
  header checksum.
- The 4-byte header is the bytes `0x4C 0x46 0x01 0x00`: magic `LF`, LanFrame
  version 1, flags 0.
- The original packet is the full IPv4 broadcast or multicast packet captured
  from the adapter, including its own header.
- The sender sends one copy to each online member that announced the
  `CapLanFrame` capability (service). Each copy is a separate unicast packet.
- The receiver checks that the outer source is the virtual IP that belongs to
  the tunnel the packet arrived on (the normal source filter), strips the two
  headers, **rewrites the inner source address to the sender's virtual IP**,
  fixes the checksums, and writes the packet to the adapter.
- The inner packet can be at most 1356 bytes (1380 minus the 24 bytes of
  headers) so that the outer packet fits the virtual MTU.

Because the outer packet is a tunnel packet, every LanFrame is end-to-end
encrypted and authenticated like any other tunnel traffic.

## 9. Signed member list

A network is described by one owner-signed list. Members keep the newest
list, and exchange it with MembershipUpdate messages. The format is
implemented in `internal/network`.

### 9.1 Address plan

- Prefix `10.77.0.0/24`. The owner is the first member and gets `10.77.0.2`.
- Usable member addresses are `10.77.0.2` to `10.77.0.254`: at most **253**
  members. `10.77.0.1` is unused and `10.77.0.255` is the subnet broadcast.
- A member keeps its address for as long as it stays in the list. A new member
  gets the lowest free address.

### 9.2 Encoding

```
magic[4] = "LFML"
format_version[1] = 1
network_id[16]
name_length[1] | name                         (UTF-8, at most 64 bytes)
owner_public_key[32]
version[8]
member_count[2]                               (1..253)
member_count * {
    public_key[32]
    virtual_ip[4]
    nickname_length[1] | nickname             (UTF-8, at most 32 bytes)
}
relay_count[1]                                (0..3)
relay_count * {
    host_length[1] | host                     (at most 253 bytes)
    udp_port[2]
    tcp_port[2]                               (0 = no TCP fallback)
}
signature[64]
```

The bytes before `signature` are the **body**. The signature is an Ed25519
signature by the owner key over

```
"lanfolk/v1/member-list" | 0x00 | body
```

The encoding is canonical: a decoder rejects trailing bytes. Members are
written in the order of the list (the owner's software keeps them sorted by
virtual IP).

### 9.3 Validation

A decoder accepts a list only if all of these hold:

- the magic and format version match;
- the signature verifies with `owner_public_key`;
- there are 1-253 members, with no duplicate key and no duplicate virtual IP;
- every virtual IP is an IPv4 address in `10.77.0.2`-`10.77.0.254`;
- the owner is a member;
- there are at most 3 relays; every relay host is non-empty, at most 253
  bytes, and contains only letters, digits, `.`, `-` and `:`; every UDP port is
  non-zero;
- every text field is valid UTF-8 within its limit.

### 9.4 Versions

- A new network has version 1. Every change (add or remove a member, set
  relays, rename) increases the version by 1 and re-signs.
- A received list replaces the stored list only if it has the same network ID
  and owner and a **higher version**. Versions never roll back.
- Only the owner can change membership. The owner cannot be removed.
- Removing a member closes its tunnel once the new list is applied.
- The relays in the list are owner-chosen shared relays. Every member
  registers at them in addition to its own relays (decision D-015).

## 10. DHT record, connection code and invite code

The DHT fallback (decisions D-010, D-013, D-014) lets two friends find each
other's addresses when no shared relay is reachable. A connection code does
the same by hand.

### 10.1 DHT record

The DHT is the BitTorrent Mainline DHT, with BEP 44 mutable items. It runs
**only while no shared relay has been reachable for 30 s**, and stops when a
relay is reachable again. The node is a passive, read-only client (BEP 43): it
does not answer queries from other nodes. It uses its own random UDP port, not
the shared socket.

Keys rotate every UTC day. For a record published on day `E`
(`E = floor(unix_seconds / 86400)`), both peers compute

```
pair_E = pair.ForDHTEpoch(E)     // secret_E = HKDF-Expand(secret, "lanfolk/v1/dht-epoch" | E as u32, 32)
salt   = DHTSalt(publisher)      // 16 bytes, from pair_E
sign   = DHTSigningKey(publisher) // Ed25519 key from pair_E
key    = DHTKey(publisher)       // 32 bytes, from pair_E
```

where `publisher` is the Ed25519 public key of the user who publishes the
record. The BEP 44 item is:

- `k` = the public key of `sign`, `salt` = `salt`, target =
  SHA-1(`k` | `salt`) as defined by BEP 44;
- `v` = a bencoded byte string whose content is
  `nonce[24] | XChaCha20-Poly1305(plaintext)` with key `key` and associated
  data `salt` (16 bytes);
- `seq` = the maximum of the publication time in unix seconds and the highest
  sequence number already on the DHT plus 1;
- signed with `sign` as BEP 44 specifies.

Plaintext:

```
version[1] = 0x01 | unix_seconds[8] | count[1] | count * address
```

- At most **32** candidates. The bencoded value must be at most **1000 bytes**
  (BEP 44), which 32 IPv6 candidates satisfy (about 660 bytes).
- Every address is non-empty (family 4 or 6) and no bytes may follow the last
  address.
- A reader opens the record with the keys of today, yesterday and tomorrow in
  turn (clock skew), checks the version, and may reject records older than a
  maximum age. A record more than about a day old cannot be opened at all.
- Lookups try the publisher's current day first, then the previous day.
- Records are published when the candidates change and at least hourly while
  the DHT is running (service).

Since the salt, the signing key and the target change daily and are derived
from the pair secret, storing nodes can neither read a record nor follow a pair
across days. They do see the IP addresses of the publisher and the reader.

### 10.2 Connection code

A connection code carries a few of the sender's candidate addresses to one
friend, encrypted, for a limited time. The user pastes it into a chat. It is
the last resort when neither the relay nor the DHT works.

```
code = "lanfolk-c1" | z-base32(binary)

binary = sender_key_id[8] | expiry_unix[4] | nonce[24] |
         XChaCha20-Poly1305(count[1] | count * address)
```

- Key: `pair.ConnCodeKey(sender)` (section 3.4).
- Associated data: `sender_key_id | expiry_unix` (12 bytes).
- At most **6** candidates, at least 1. The expiry is an absolute unix time that
  must fit in 32 bits.
- z-base32 is the friend code alphabet, no padding. When parsing, spaces,
  tabs, line breaks and `-` are removed and letters are lowercased; the
  literal prefix without its dash (`lanfolkc1`) is matched against the
  cleaned text.
- The receiver looks up the pair by `sender_key_id` among its friends (unknown
  sender: rejected), checks that the key ID really belongs to that friend,
  decrypts, and rejects an expired code or one with no or malformed
  addresses.

### 10.3 Invite code

An invite code lets a friend join a private network with one pasted string
instead of exchanging friend codes in both directions and waiting for the
owner to add the new member (decision D-025).

```
code = "lanfolk-i1" | z-base32(binary)

binary = owner_key[32] | network_id[16] | token[16] | expiry_unix[4] | checksum[4]
checksum = SHA-256("lanfolk-i1" | owner_key | network_id | token | expiry_unix)[0:4]
```

- `token` is 128 random bits chosen by the owner. It is a bearer secret: whoever
  holds the code can ask to join, like a Hamachi password.
- z-base32 is the friend code alphabet, no padding (116 characters). When
  parsing, spaces, tabs, line breaks and `-` are removed, letters are
  lowercased and the literal prefix without its dash (`lanfolki1`) is matched.
  The remaining text must decode to exactly 72 bytes, re-encode to the same
  text (one spelling per code) and carry a correct checksum, else the code is
  invalid. A code whose `expiry_unix` is not in the future is expired.
- An invite is valid for **7 days** and can be used by any number of people
  until it expires or the owner resets it. The owner keeps one invite per
  owned network; asking for it returns the current code while it is valid.
  A **reset** (or an expired code) creates a new token, which makes the old
  code useless because nobody serves its session any more.

**Session ID and key.** From the token both sides derive, with
`HKDF-SHA256(ikm = token, salt = "lanfolk/v1/invite", info = label | owner_key | network_id)`:

| Value | Label | Length |
| --- | --- | --- |
| Invite session ID (relay and control frames) | `lanfolk/v1/invite/session` | 16 |
| Invite key (symmetric, both directions) | `lanfolk/v1/invite/key` | 32 |

**Rendezvous.** The owner registers the invite session at all its relays
(section 4.6) while it has an unexpired invite, whether or not that network is
the active one. A joiner that redeems the code registers the same session. A
control frame of type `0xF2` (section 5.1) with the invite session ID, sealed
with the invite key, carries the messages. The relay needs no change: it sees
two parties in a session, as for a pair. The invite code carries no relay
hints, so owner and joiner meet at a relay both of them use, normally the
build's default relay. Direct paths are not used for invite sessions.

**JoinRequest.** After `PeerJoined` or `peer_present = 1` the joiner sends
JoinRequest to every relay where the owner is present and repeats it every
**3 s** while the join is not accepted, until the invite expires. It also
registers the session again at every relay where the owner is not present, so
that a `SessionFull` rejection (another joiner is inside) is retried.
`signature` is the joiner's Ed25519 signature over

```
"lanfolk/v1/join-request" 0x00 | invite_session[16] | network_id[16] | joiner_key[32] | timestamp[8]
```

The owner ignores the request, without a reply, unless all of this holds: the
frame opens with the invite key; the invite is the owner's current, unexpired
one for that network; `network_id` matches; `joiner_key` is not the owner's;
`timestamp` is within **+/-10 minutes** of the owner's clock; the signature
verifies; at most **30 new joiners per invite per hour** (a repeated request
from a joiner that is already pending does not count; a reset clears the
count); and the network has a free address. Valid frames are handled at most
once per 200 ms per invite session.

**Owner behavior.** For a valid request the owner adds the joiner as a friend
(if it is not one) and marks the friend with the network as pending. As soon as
the friendship is active (mutual Hello with the friend flag), it adds the joiner
to the member list, which the normal membership machinery pushes. If the joiner
is a member already, the owner only answers. The answer is JoinAccepted, sealed
with the invite key and sent to the relay the request came from. `signature` is
the owner's Ed25519 signature over

```
"lanfolk/v1/join-accepted" 0x00 | invite_session[16] | network_id[16] | joiner_key[32]
```

**Joiner behavior.** On redeeming, the joiner adds the owner as a friend at
once (so the pair session is registered and the friendship can be confirmed),
records a pending join, and registers the invite session. When a JoinAccepted
with a valid owner signature arrives it leaves the invite session and waits
for the member list, which arrives over the pair session and activates the
network if no other is active. A join is listed with the state `waiting` (owner
not answered yet), `joining` (accepted, list on its way), `joined` or `expired`.
Pending joins and owned invites are stored and survive restarts of the service.

**Limitations.** The owner must be online during the exchange; joining waits
otherwise. The invite session holds two parties, so a second simultaneous
joiner is rejected with `SessionFull` and gets in once the first has left the
session (a joiner leaves as soon as it is accepted). A client that exits
cleanly unregisters its sessions; after a crash the relay keeps the old party
until it has been silent for 30 s (section 4.3).

**Removal (service).** Removing a member drops the network's invite at once,
so a removed person cannot come back with the code they used; the owner's next
request creates a new code. A friendship that only an invite created (on
either side) ends by itself once the two no longer share a network and no join
between them is in progress. On the owner's side this happens 90 s after the
removal, so that a lost member list still reaches the removed member. Friends
added by hand are never removed this way; adding an invite friend by hand
makes the friendship permanent.

### Address encoding

Everywhere in this document an address is

```
family[1] | port[2] | IP
```

`family` 4 is followed by a 4-byte IPv4 address (7 bytes in total), family 6 by
a 16-byte IPv6 address (19 bytes in total). **Family 0 means "no address"** and
is a single byte, with no port or IP. IPv4-mapped IPv6 addresses are encoded as
IPv4. Any other family value makes the message invalid.

## 11. Local control protocol

The service, which runs as LocalSystem, and its clients (the desktop UI and
`lanfolk-cli`) talk through a local control protocol. It never leaves the
machine.

### 11.1 Transport and security

- Transport: the named pipe `\\.\pipe\lanfolk`. The environment variable
  `LANFOLK_PIPE` overrides the name for tests and side-by-side builds. (On
  other platforms the implementation uses a Unix socket for tests.)
- The pipe's security descriptor is `D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;IU)`:
  full access for LocalSystem and Administrators; interactive users may read
  and write data and attributes (`0x12019b`) but not append, which on a pipe
  means creating another server instance. A listener that does not run as
  LocalSystem (tests, development) also grants full access to its own user.
- A client opens the pipe with exactly that access and the identification
  impersonation level. When it uses the default name it checks that the
  pipe's owner is LocalSystem or Administrators, so a pipe of the same name
  created by another user while the service was stopped is refused.
- The service identifies the client by impersonating it after the first
  request line has arrived (Windows allows this only after data was read),
  not by process ID. Only the user recorded at installation as the **pipe
  owner**, or an elevated administrator, may continue; anyone else gets one
  error message with code `unauthorized` and the connection is closed. A
  connection that sends no request within 30 s is closed.

### 11.2 Framing

One JSON object per line (UTF-8, `\n` terminated), at most 1 MiB per line.

```
request:   {"id":1,"method":"friends.add","params":{"code":"lanfolk1..."}}
response:  {"id":1,"result":{...}}
error:     {"id":1,"error":{"code":"invalid","message":"..."}}
event:     {"event":"peers","data":[...]}
```

- `id` is chosen by the client (non-zero) and echoed in the response. Methods
  are handled in order per connection.
- A line that is not valid JSON is answered with an `invalid` error without an
  `id`.
- After `subscribe`, the service pushes events on the same connection.
  Events carry no `id`. A client that does not read events fast enough loses
  old events; replies are never blocked.

Error codes: `invalid` (bad parameters), `not_found` (unknown friend, network
or member), `forbidden` (for example, only the owner can change membership),
`unauthorized` (the pipe client is not the session owner), `internal`,
`unavailable` (for example, the adapter could not be created).

### 11.3 Methods

Only these methods exist. Parameters are validated by the service. Types are
those in `internal/ipc/api.go`; field names are the JSON names.

| Method | Parameters | Result | Purpose |
| --- | --- | --- | --- |
| `status` | none | `Status` | Service state |
| `subscribe` | none | `true` | Start receiving events |
| `identity.setNickname` | `{nickname}` | none | Change the display name |
| `identity.export` | `{password}` | `{backup}` | Export an encrypted identity backup (`lanfolk-backup1:...`) |
| `identity.import` | `{backup, password}` | none | Replace the identity from a backup |
| `friends.list` | none | `[Friend]` | List friends |
| `friends.add` | `{code, nickname?}` | `Friend` | Add a friend by friend code (state `pending` until the other side added us) |
| `friends.remove` | `{public_key}` | none | Remove a friend |
| `networks.list` | none | `[Network]` | List networks |
| `networks.create` | `{name}` | `Network` | Create a network owned by the user |
| `networks.addMember` | `{network_id, public_key}` | `Network` | Owner adds an active friend |
| `networks.removeMember` | `{network_id, public_key}` | `Network` | Owner removes a member |
| `networks.rename` | `{network_id, name}` | `Network` | Owner renames the network |
| `networks.setRelays` | `{network_id, relays}` | `Network` | Owner sets up to 3 relays (`[{host, udp_port, tcp_port}]`) |
| `networks.activate` | `{network_id}` | none | Make this the active network |
| `networks.leave` | `{network_id}` | none | Leave a network |
| `peers.list` | none | `[Peer]` | Live state of the active network's members |
| `settings.get` | none | `Settings` | Read settings |
| `settings.set` | `Settings` | `Settings` | Change settings (`default_relay` is read-only) |
| `conncode.create` | `{public_key}` | `{code, expires}` | Create a connection code for a friend |
| `conncode.use` | `{code}` | none | Use a connection code received from a friend |
| `diagnostics.export` | none | `{report}` | Text report for bug reports |
| `invites.create` | `{network_id, reset?}` | `{code, expires}` | Owner gets the invite code of a network (the current one while valid; `reset: true` makes a new one) |
| `invites.redeem` | `{code}` | `{network_id, owner_public_key, state}` | Start joining with an invite code (`state` is `waiting`, or the state of an identical join already in progress) |
| `invites.list` | none | `[PendingJoin]` | Invite codes the user redeemed and their progress |
| `invites.cancel` | `{network_id}` | none | Stop waiting for an invite (an owner who became a friend only through the invite is removed again unless a network is shared) |
| `service.shutdown` | none | none | Stop the service after the reply, as the user asked ("Turn off Lanfolk"); it stays an automatic service and starts again with Windows or when the app starts it (D-039) |

For methods whose result or parameter type the Go declaration does not state
(`identity.setNickname`'s result, `networks.*`'s result, `conncode.create`'s
parameter, ...), the entry above is the intended contract of the service
that is being written alongside this document; `internal/ipc/api.go` declares
the parameter and result structs and is authoritative for the field names.

### 11.4 Types

`Status`:

| Field | Type | Meaning |
| --- | --- | --- |
| `version` | string | Service version |
| `nickname` | string | |
| `public_key` | string | Hex |
| `friend_code` | string | The user's friend code |
| `relays` | `[RelayStatus]` | Effective relay list |
| `active_network_id`, `active_network_name`, `virtual_ip` | string, optional | Active network |
| `adapter_up` | bool | |
| `adapter_error` | string, optional | |
| `listen_port` | int | UDP port of the shared socket |
| `port_mapping` | string, optional | For example `upnp` |
| `dht` | string | `off`, `standby` or `running` |
| `relay_only` | bool | |
| `compat_mode` | bool | |
| `warnings` | `[string]`, optional | |

`RelayStatus`: `name` (host:port), `source` (`default`, `network` or
`custom`), `mode` (`udp`, `tcp` or `down`), `rtt_ms`.

`Friend`: `public_key` (hex), `friend_code`, `nickname`, `state` (`pending` or
`active`), `online`, `added_at`, `last_seen_at` (optional). Times are RFC 3339.

`Network`: `id` (hex), `name`, `owner_key`, `is_owner`, `active`, `version`,
`members` (`[Member]`), `relays` (`[Relay]`, optional).

`Member`: `public_key`, `nickname`, `virtual_ip`, `is_self`, `is_owner`,
`is_friend`. `Relay`: `host`, `udp_port`, `tcp_port`.

`Peer`: `public_key`, `nickname`, `virtual_ip`, `online`, `path` (`direct`,
`member-relay`, `shared-relay`, `tcp-fallback` or `none`), `relay_detail`
(relaying member's nickname or the relay name), `ping_ms`, `avg_ping_ms`,
`jitter_ms`, `loss_pct`, `quality` (`good`, `fair`, `poor` or `unknown`),
`version` (the member's Lanfolk release from its Hello; empty if unknown).

`PendingJoin`: `network_id`, `network_name` (optional, known once the owner
accepted), `owner_public_key`, `owner_nickname` (optional), `state`
(`waiting`, `joining`, `joined` or `expired`), `expires` (RFC 3339; while
`joining` it is the time after which the join is given up). Entries stay in
the list for a day after they end. A UI shows `waiting` as "Waiting for
<owner> to come online", `joining` as "Joining..." and shows the network at
`joined`.

`Settings`: `relay_only`, `compat_mode`, `manual_port` (0 = automatic),
`port_mapping`, `dht_fallback`, `custom_relays` (`[Relay]`), `default_relay`
(read-only), `notifications` and `check_updates` (desktop app preferences,
both on by default), `allow_services` (members may reach ports that only
Windows services listen on; off by default, see D-033).

### 11.5 Events

| Event | Data | When |
| --- | --- | --- |
| `status` | `Status` | On change |
| `peers` | `[Peer]` | Every second while a network is active |
| `friends` | `[Friend]` | On change |
| `networks` | `[Network]` | On change |
| `invites` | `[PendingJoin]` | On change of any pending join |

## 12. Change log

| Date | Version | Change |
| --- | --- | --- |
| 2026-11-04 | 1 | First complete specification: demultiplexing, identity and key derivation, shared relay (D-007), control frames, path selection, member relay, LanFrame, signed member list with relays (D-015), DHT record and connection code (D-010, D-013, D-014), local control protocol. |
| 2026-11-04 | 1 | Invite codes (D-025): `lanfolk-i1` code, invite session and key derivation, control message types 8 and 9 (JoinRequest, JoinAccepted), IPC methods `invites.*` and event `invites`. A compatible addition: types 8 and 9 travel only in invite sessions, so clients that do not know them never see them. |
| 2026-11-04 | 1 | Relay hardening (compatible): optional Register cookie and control type `0x0B` Challenge, sent only while the relay is under pressure (4.3.1); per-IP and total relay data limits, a total TCP connection limit, closing of silent TCP connections without a session, a per-IP limit on delayed echoes, per-/64 counting for IPv6 (4.5). |
| 2026-11-04 | 1 | Local control hardening (D-033): pipe descriptor without instance creation for interactive users, client owner check, identification by impersonation after the first request, first-request timeout; setting `allow_services`. Clients and service are installed together, so an older client cannot open the new pipe. |

Rules for changes: a change that old and new clients cannot both understand
increases the protocol version announced in Hello and is described here before
the code changes. A compatible addition (a new optional capability bit, a new
local control method) is added to the relevant section and listed above.
