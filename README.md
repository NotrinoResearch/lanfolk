# Lanfolk

Lanfolk is an open-source virtual LAN for playing LAN games with friends over
the internet. Each member of a private network gets a virtual IP on a Windows
virtual adapter, and games see each other as if they shared a local network,
even when every player is behind CGNAT and cannot open ports.

> Status: under active development. See the
> [implementation plan](docs/lanfolk-open-source-virtual-LAN-implementation-plan.md)
> for progress and [docs/DECISIONS.md](docs/DECISIONS.md) for deviations.

## How it works

- Friends add each other with friend codes, which carry their public keys.
- A network owner creates a private network and adds friends. Members connect
  in a full mesh over WireGuard-compatible encrypted tunnels.
- A direct path is always preferred. If hole punching fails, traffic goes
  through another member or through a shared relay.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/lanfolk-relay` | Shared relay (STUN, signaling, fallback forwarding) |
| `internal/identity` | Keys, friend codes and pair secrets |
| `internal/network` | Signed member lists and virtual IPs |
| `internal/transport` | Shared UDP socket, WireGuard bind, relay client |
| `internal/relay` | Relay server |
| `deploy/relay` | Dockerfile, compose file and `.env.example` |
| `docs` | Plan, protocol, decisions and measurements |

## License

Apache-2.0. See [LICENSE](LICENSE).
