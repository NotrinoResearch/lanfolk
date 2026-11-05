# LAN compatibility

Lanfolk does not know games. It contains no game names, profiles or per-game port lists. It reproduces the behavior of a local IPv4 network, and a game works when the network behaviors it uses are available. This page lists those behaviors and how Lanfolk handles each one. It is informational: it describes what Lanfolk does, not which games are allowed.

## Behavior classes

| Behavior | Where it appears | How Lanfolk handles it | Scope |
| --- | --- | --- | --- |
| TCP and UDP unicast | Direct connections to a host | Every member has a virtual IP in `10.77.0.0/24`; all ports and both protocols pass through the tunnel, except the blocked system ports below | First release |
| Subnet broadcast (`10.77.0.255`) | LAN game discovery | Datagrams sent to the subnet broadcast address reach the adapter, are copied to every online member as LanFrames and delivered with the sender's virtual IP as source | First release |
| Global broadcast (`255.255.255.255`) | Server browsers, discovery | Windows sends these through the preferred interface only. Lanfolk captures the local copy by default (capture mode) and sends it to every online member with the sender's virtual IP as source and a recomputed checksum (D-034); compatibility mode lowers the adapter metric so Windows uses the virtual adapter | First release |
| Multicast | Local discovery protocols (for example `239.x.x.x`) | Group traffic on the adapter is copied to online members as LanFrames. Compatibility mode helps when Windows chooses another interface for the group | First release |
| IP address in the payload | Games or protocols that announce their own IP | The game must announce the virtual IP. Compatibility mode makes the virtual adapter the preferred interface, which helps these protocols choose the virtual IP. DirectPlay games behind a UPnP router announce the router's external address instead; while a network is active Lanfolk turns DirectPlay's UPnP helper off for the user, so they announce no address and members answer the virtual IP (D-038) | First release |
| Private IP checks | Games that accept only LAN addresses | `10.77.0.0/24` is a private (RFC 1918) range | First release |
| Peer-to-peer game traffic | Serverless games, mesh games | Network members are connected in a full mesh; each pair has its own tunnel | First release |
| Internet authentication | Game accounts, license checks, anti-cheat | Lanfolk does not touch normal internet traffic. Whether the game works without or with its online services is up to the game | Game dependent |
| Non-IP protocols | Old IPX or raw Ethernet games | The tunnel is Layer 3 (IPv4). These protocols cannot be carried | Outside the first release |

Other facts that matter for compatibility:

- **IPv4 only.** The virtual network has no IPv6. Lanfolk disables IPv6 on its adapter where Windows allows it.
- **MTU 1380.** Games should work without change; this leaves room for the tunnel and for relay headers.
- **System ports are blocked between members.** A network places members on the same LAN, so Lanfolk blocks TCP 135, 139, 445, 593, 2869, 3389, 5355, 5357-5358, 5985-5986, 10243 and UDP 137-138, 1900, 3389, 3702, 5355 to and from the virtual network (file sharing, remote desktop, remote management, name resolution and device discovery).
- **Windows services are out of reach.** Members reach the games and other programs you run on your desktop, on any port. Programs that run as Windows services (remote access, media or database servers) are not reachable through Lanfolk. If a game's dedicated server runs as a Windows service, turn on **Settings, Troubleshooting, Let members reach Windows services** (D-033).
- **No default route.** Only `10.77.0.0/24` uses the adapter.
- **One active network.** A user is active in one network at a time, so the adapter and the address range stay unambiguous.

## What Lanfolk does when a game does not work

A failing game is a missing network behavior, not a missing profile:

1. Run `lanprobe test` between the two machines (see [TESTING.md](TESTING.md)). It checks each behavior in the table without a game.
2. If a behavior fails, the fix belongs in Lanfolk's general handling of that behavior.
3. Game-specific rules are never added.

## Reported games

Players and testers may report games they tried. This table is a record of reports. **It does not limit support**: a game that is not listed may work, and a game listed as not working describes one setup at one time.

| Game | Version / date | Behavior class(es) used | Result | Notes |
| --- | --- | --- | --- | --- |
| Commandos 2: Men of Courage (DirectPlay 4) | 2026-11-05, Lanfolk 0.4.1 development build | Discovery by global broadcast (UDP 47624), TCP 2300, UDP 2350 | Works between two homes, capture and compatibility mode | Lanfolk 0.4.0 found no session in capture mode (D-034); a joiner behind a UPnP router needs Lanfolk 0.4.3 (D-038) |

To add a row, open an issue or a pull request with the game, the result, which behaviors it uses (discovery by broadcast, join by IP, and so on), whether compatibility mode was needed, and the `lanprobe test` result. Do not include IP addresses or friend codes.
