<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/logo-on-dark.svg">
    <img src="assets/brand/logo.svg" alt="Lanfolk" height="56">
  </picture>
</p>

<p align="center">Play LAN games with your friends over the internet.</p>

<p align="center">
  <a href="https://github.com/NotrinoResearch/lanfolk/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/NotrinoResearch/lanfolk?include_prereleases&label=release"></a>
  <img alt="Windows 10 and 11" src="https://img.shields.io/badge/platform-Windows%2010%20%7C%2011-2F5BEA">
  <a href="LICENSING.md"><img alt="License: PolyForm Noncommercial 1.0.0" src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-14C38E"></a>
</p>

Lanfolk puts you and your friends on one private network, so a game's LAN
mode works as if you were in the same room. One of you creates a network and
sends an invite code; the others paste it and join. You don't need to open
ports on your router, and it works behind mobile and CGNAT connections too.

<p align="center">
  <img src="docs/images/main-window.png" width="300" alt="The Lanfolk window: your status and IP address, a network with four members, their ping and connection type">
  &nbsp;
  <img src="docs/images/settings-window.png" width="520" alt="The Lanfolk Settings window on the Relays page">
</p>
<p align="center"><sub>Screenshots with sample data.</sub></p>

## Download

Get the installer from the [Releases page](https://github.com/NotrinoResearch/lanfolk/releases).
You need Windows 10 or 11, 64-bit, and administrator rights to install.

Lanfolk is in beta. The installer is not code signed yet, so Windows
SmartScreen may say it "protected your PC". Choose **More info** and then
**Run anyway**. Each release lists SHA-256 checksums if you want to check the
file first.

## Getting started

1. Run the installer and open Lanfolk. It asks for the name your friends will see.
2. One person chooses **Create**, names the network and copies the invite code.
3. Everyone else chooses **Join network** and pastes the code. The person who
   created the network has to be online at that moment.
4. Start the game and look for the others in its LAN or multiplayer browser.

Closing the window keeps Lanfolk running in the notification area next to the
clock. The [user guide](docs/SETUP.md) covers the rest, including what to do
when a game doesn't find the other players.

## What it does

- Every member gets an address like `10.77.0.3` on a virtual network
  adapter. Games see each other as if they shared a home network.
- LAN discovery works: broadcast and multicast traffic reaches the other
  players, and compatibility mode helps games that send it the old way.
- Connections go straight between players when possible and fall back to
  another member or a relay server when not. Traffic is encrypted with
  WireGuard; a relay only forwards packets it cannot read.
- Only traffic for the virtual network goes through Lanfolk. Your normal
  internet traffic doesn't, and Windows file sharing and remote desktop stay
  blocked between members.
- There are no accounts. Your identity is a key on your computer, and only
  people with your invite can join your network.

## Is it free?

Yes, for personal and other noncommercial use. Lanfolk is made by
NotrinoResearch and licensed under the
[PolyForm Noncommercial License 1.0.0](LICENSE). Companies, LAN centers and
anyone who wants to sell or bundle it need a commercial license. See
[LICENSING.md](LICENSING.md).

## Status

Lanfolk 0.2 is a beta. It has been tested on Windows 11 between two machines
behind separate NATs, over a direct path and through the relay; the
measurements are in [docs/MEASUREMENTS.md](docs/MEASUREMENTS.md). Tests with
more internet providers, real games and Windows 10 are still open and tracked
in [docs/TODO.md](docs/TODO.md).

Found a bug or a game that doesn't work? Please
[open an issue](https://github.com/NotrinoResearch/lanfolk/issues) and attach
the diagnostic report from **Settings, Troubleshooting**. Report security
problems privately as described in [SECURITY.md](SECURITY.md).

## For developers

Lanfolk is written in Go. The parts:

- `lanfolk-service`, a Windows service that runs the network engine
  (wireguard-go with a custom bind, Wintun, UDP hole punching, broadcast
  copying, port mapping, a DHT fallback);
- `Lanfolk.exe`, the desktop app (Wails) with its tray icon, and
  `lanfolk-cli`, both talking to the service over a named pipe;
- `lanfolk-relay`, the relay server (STUN, signaling, encrypted forwarding
  over UDP and TCP), shipped as a Docker image;
- `natprobe` and `lanprobe`, measurement tools.

The protocol is specified byte by byte in [docs/PROTOCOL.md](docs/PROTOCOL.md).

### Build and test

You need Go (the version in `go.mod`). Tests with `-race` need cgo, so on
Windows install a MinGW-w64 GCC.

```sh
go build ./...
go test -race ./...
```

A release build runs on Windows with PowerShell:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build-release.ps1 -Version 1.0.0
```

It builds every binary, downloads and verifies `wintun.dll`, regenerates
`THIRD_PARTY_NOTICES.md`, builds the installer with Inno Setup 6.7 or newer
(if it finds `ISCC.exe`; `-Iscc <path>` points to it) and writes `SHA256SUMS`
into `dist\`.

The default relay is not stored in the repository. Put the one you want to
build in into an untracked `build.env` in the repository root:

```dotenv
LANFOLK_DEFAULT_RELAY_HOST=relay.example.org
LANFOLK_DEFAULT_RELAY_UDP_PORT=3478
LANFOLK_DEFAULT_RELAY_TCP_PORT=443
```

Or pass `-NoDefaultRelay`; users can add relays in Settings. To run your own
relay with Docker, see [docs/SELF_HOST.md](docs/SELF_HOST.md).

`Lanfolk.exe -mock` opens the app against a built-in fake service, which is
handy for UI work. A build with `-tags automation` adds a local API for
scripted UI checks (`scripts/dev/ui.mjs`).

<details>
<summary>Repository layout</summary>

| Path | Contents |
| --- | --- |
| `cmd/lanfolk-service`, `cmd/lanfolk-cli`, `ui` | Windows service, command line client, desktop app |
| `cmd/lanfolk-relay`, `deploy/relay` | Relay server and its Docker files |
| `cmd/natprobe`, `cmd/lanprobe` | NAT and LAN measurement tools |
| `internal/engine` | Client core that ties the packages together |
| `internal/transport`, `internal/wire`, `internal/pathmgr` | UDP socket, WireGuard bind, relay client, packet formats, path selection |
| `internal/tunwrap`, `internal/winnet` | Broadcast and multicast copying, port filter; Windows adapter, metric and firewall |
| `internal/identity`, `internal/network`, `internal/invite` | Keys and friend codes, signed member lists, invite codes |
| `internal/peermsg`, `internal/discovery`, `internal/portmap` | Peer messages, DHT and connection codes, UPnP/NAT-PMP/PCP |
| `internal/relay`, `internal/stun` | Relay server, STUN messages |
| `internal/ipc`, `internal/store`, `internal/version`, `internal/clock` | Service control protocol, local data file, build info, timing |
| `installer`, `assets/brand`, `scripts` | Inno Setup script, logo and icons, build scripts |
| `docs` | Guides, protocol, tests, measurements, decisions |

</details>

### Documentation

| Document | What it covers |
| --- | --- |
| [docs/SETUP.md](docs/SETUP.md) | User guide and troubleshooting |
| [docs/SELF_HOST.md](docs/SELF_HOST.md) | Running your own relay |
| [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) | Which LAN behaviors Lanfolk carries |
| [docs/PROTOCOL.md](docs/PROTOCOL.md) | Wire protocol |
| [docs/TESTING.md](docs/TESTING.md) | Manual test procedures |
| [docs/MEASUREMENTS.md](docs/MEASUREMENTS.md) | Measured results |
| [docs/TODO.md](docs/TODO.md) | Open work and remaining tests |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Design decisions and why |
| [Implementation plan](docs/lanfolk-open-source-virtual-LAN-implementation-plan.md) | Original design and progress |
| [DESIGN.md](DESIGN.md) | UI design rules |

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first;
pull requests need the [Contributor License Agreement](CLA.md). Everyone
taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Copyright (c) 2026 NotrinoResearch. Licensed under the
[PolyForm Noncommercial License 1.0.0](LICENSE); commercial licenses are
available ([LICENSING.md](LICENSING.md)). Versions up to the tag
`apache-2.0-final` were released under Apache-2.0. Third-party components keep
their own licenses ([THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)).

WireGuard is a registered trademark of Jason A. Donenfeld. Lanfolk is not
affiliated with the WireGuard project.
