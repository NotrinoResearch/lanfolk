# Running your own relay

The Lanfolk relay is a small Go program that runs in a Docker container. Lanfolk clients use it to learn their public address (STUN), to find each other, and as the last-resort path when no direct connection or member relay works. You can run your own relay for a private group, for a region where the default relay is far away, or to avoid depending on someone else's server.

The relay is stateless. It has no accounts, no database and writes nothing to disk. For how it works at the byte level, see [PROTOCOL.md](PROTOCOL.md#4-shared-relay-protocol).

## Requirements

- A Linux server with a public IPv4 address that is reachable from the internet. The relay does not need to be in a particular country, but players get the best latency when it is close to them: relayed traffic has the sum of both players' latency to the server (see [MEASUREMENTS.md](MEASUREMENTS.md)).
- A current Docker Engine with the Compose plugin (`docker compose`). The image is `FROM scratch` and about 7 MB; it needs no other software on the server.
- Free ports:

  | Port | Protocol | Use |
  | --- | --- | --- |
  | 3478 | UDP | STUN, signaling and relayed game traffic. Required. |
  | 443 | TCP | Fallback for players whose network blocks UDP. Optional. Use another free port if 443 is taken. |
  | 9100 | TCP, **localhost only** | Health check and metrics. Not exposed to the internet. |

- A rough capacity guide: each relayed session is limited to 2 Mbit/s by default, and a typical game uses far less. A small virtual server is enough for many sessions. Measure with the metrics endpoint before sizing.

The compose file uses `network_mode: host`, so the container binds the ports directly on the server and Docker adds no port-forwarding or NAT rules. This means the ports must be free **before** you start. Check them:

```sh
ss -lun | grep ':3478 ' ; ss -ltn | grep -E ':(443|9100) '
```

No output means the ports are free. If something already uses TCP 443, pick another port for the TCP fallback (for example 8443) and set `RELAY_TCP_PORT` accordingly.

## Firewall

Open the relay ports in the server's firewall and in your hosting provider's firewall or security group, if there is one. The relay does not change firewall rules itself.

With UFW, for example:

```sh
sudo ufw allow 3478/udp comment 'lanfolk-relay'
sudo ufw allow 443/tcp comment 'lanfolk-relay'   # or your TCP port
```

Do **not** open port 9100. The metrics endpoint listens on `127.0.0.1` by default and has no authentication.

## Configuration

Copy `deploy/relay/.env.example` to `deploy/relay/.env` and edit it. The `.env` file is ignored by git. Keep only relay settings in it, never SSH or hosting credentials.

| Variable | Default | Description |
| --- | --- | --- |
| `RELAY_PUBLIC_HOST` | none (required by the compose file) | The public IP address or DNS name that clients connect to. The relay only logs whether it is set; clients get the address from their own settings. |
| `RELAY_UDP_PORT` | `3478` | UDP port for STUN, signaling and data. |
| `RELAY_TCP_PORT` | `443` | TCP fallback port. `0` turns the TCP fallback off. |
| `RELAY_MAX_SESSION_KBPS` | `2000` | Per-session data rate limit in kbit/s (both directions share it). |
| `RELAY_MAX_IP_KBPS` | `20000` | Relay data one source IP (IPv6: one /64) may send over all its sessions, in kbit/s. `0` turns the limit off. |
| `RELAY_MAX_TOTAL_MBPS` | `400` | All relay data the relay forwards, in Mbit/s. Keep it below your server's bandwidth so the relay cannot crowd out other services on the same host. `0` turns the limit off. |
| `RELAY_CHALLENGE` | `auto` | Register cookies (see [PROTOCOL.md](PROTOCOL.md) 4.3.1): `auto` asks for them only under pressure, `always` asks every new UDP registration (clients before 0.4 then cannot register over UDP), `off` never asks. |
| `RELAY_METRICS_ADDR` | `127.0.0.1:9100` | Address of the health and metrics endpoint. `off` disables it (and the health check then always passes). |
| `RELAY_IMAGE_TAG` | `latest` | Image tag that compose starts. |

The relay binary reads more variables. The compose file does not pass them on by default; to use one, add it under `environment:` in `docker-compose.yml`:

| Variable | Default | Description |
| --- | --- | --- |
| `RELAY_BIND_ADDR` | all addresses | Bind the UDP and TCP sockets to one local address. |
| `RELAY_MAX_SESSIONS_PER_IP` | `600` | Parties (registered clients) allowed per source IP. |
| `RELAY_MAX_SESSIONS` | `100000` | Sessions allowed in total. |
| `RELAY_IDLE_TIMEOUT_SEC` | `120` | Seconds without a registration or data before a client is removed. |
| `RELAY_MAX_TCP_CONNS` | `2048` | TCP fallback connections in total. Each costs about 50 KB of memory; raise the compose memory limit with it. |
| `RELAY_TCP_GRACE_SEC` | `15` | Seconds a TCP connection without a session may stay silent before it is closed. |
| `RELAY_MAX_ECHOES_PER_IP` | `8` | Pending delayed echoes (used by `natprobe`) per source IP. |
| `RELAY_CHALLENGE_SESSIONS_PCT` | `50` | With `RELAY_CHALLENGE=auto`: ask for cookies while the session table is at least this full (percent of `RELAY_MAX_SESSIONS`). `0` disables this trigger. |
| `RELAY_CHALLENGE_NEW_PER_SEC` | `1000` | With `RELAY_CHALLENGE=auto`: ask for cookies while more new sessions or parties than this are requested over UDP per second. `0` disables this trigger. |
| `RELAY_LOG_LEVEL` | `info` | Set to `debug` for debug logging. |

The container runs as a non-root user with a read-only root filesystem, all capabilities dropped except `NET_BIND_SERVICE`, a memory limit of 256 MB and a limit of 256 processes. See [DECISIONS.md](DECISIONS.md) (D-006) for why `no-new-privileges` is not set.

## Build the image

Run this from the root of the repository:

```sh
docker build -f deploy/relay/Dockerfile -t lanfolk-relay:1.0.0 --build-arg VERSION=1.0.0 .
```

Use any tag you like, and set `RELAY_IMAGE_TAG` to the same value in `.env`. The build uses the `golang` image and produces a single static binary.

## Install on the server

### Option A: build on the server

Clone or copy the repository to the server, build the image as above, then continue with "Start the relay".

### Option B: build elsewhere and copy the image

Use this when the server must not pull build images or install anything else. On your build machine:

```sh
docker save lanfolk-relay:1.0.0 | gzip > lanfolk-relay-1.0.0.tar.gz
sha256sum lanfolk-relay-1.0.0.tar.gz
scp lanfolk-relay-1.0.0.tar.gz deploy/relay/docker-compose.yml deploy/relay/.env.example user@relay.example.org:/tmp/
```

On the server, create the deployment folder and load the image:

```sh
sudo mkdir -p /opt/lanfolk-relay
sudo mv /tmp/docker-compose.yml /opt/lanfolk-relay/
sudo mv /tmp/.env.example /opt/lanfolk-relay/.env
sha256sum /tmp/lanfolk-relay-1.0.0.tar.gz            # compare with the value from your build machine
gunzip -c /tmp/lanfolk-relay-1.0.0.tar.gz | sudo docker load
rm /tmp/lanfolk-relay-1.0.0.tar.gz
```

Edit `/opt/lanfolk-relay/.env`: set `RELAY_PUBLIC_HOST`, the ports and `RELAY_IMAGE_TAG=1.0.0`.

## Start the relay

```sh
cd /opt/lanfolk-relay
sudo docker compose -p lanfolk-relay up -d
```

The compose project name is `lanfolk-relay`. The container is named `lanfolk-relay` and restarts automatically (`restart: unless-stopped`), including after a reboot.

## Check that it works

Container health (the image checks its own `/healthz` endpoint every 30 seconds):

```sh
sudo docker ps --filter name=lanfolk-relay
sudo docker inspect --format '{{.State.Health.Status}}' lanfolk-relay    # healthy
curl -s http://127.0.0.1:9100/healthz                                    # ok
curl -s http://127.0.0.1:9100/status                                     # JSON counts
```

From another computer, test the UDP side with `natprobe`, which sends STUN and relay pings to your server and also tests the TCP port:

```sh
natprobe -relay relay.example.org -relay-udp-port 3478 -relay-tcp-port 443 -skip-dht
```

The "Relay UDP RTT" and "Relay TCP RTT" lines must show answers. In the Lanfolk app, the relay appears in the connection panel as `udp` (or `tcp` when only the TCP fallback works), with its round trip time.

## Metrics and logs

The endpoint on `RELAY_METRICS_ADDR` serves:

| Path | Content |
| --- | --- |
| `/healthz` | `ok` with status 200 while the relay runs |
| `/status` | JSON: uptime, sessions, parties, TCP connections, pending echoes |
| `/metrics` | Prometheus text format, metrics named `lanfolk_relay_*` |

Useful metrics: `lanfolk_relay_sessions`, `lanfolk_relay_parties`, `lanfolk_relay_tcp_connections`, `lanfolk_relay_forwarded_packets_total`, `lanfolk_relay_forwarded_bytes_total`, `lanfolk_relay_rejects_total`, and the `lanfolk_relay_dropped_*_total` counters (rate limits, unregistered sources, malformed packets, full TCP queues, the per-IP and total data limits). `lanfolk_relay_under_pressure` is 1 while new UDP registrations need a cookie; `lanfolk_relay_challenges_total`, `lanfolk_relay_cookie_rejects_total`, `lanfolk_relay_tcp_rejects_total`, `lanfolk_relay_tcp_idle_closed_total` and `lanfolk_relay_echo_rejects_total` show the protections at work. A rising `dropped_ip_rate` or `under_pressure` without a matching rise in users is a sign of abuse. Point a Prometheus scraper at the endpoint through an SSH tunnel or a local agent; do not expose it publicly.

Logs are JSON lines on standard output, rotated by Docker (3 files of 10 MB):

```sh
sudo docker logs --tail 100 -f lanfolk-relay
```

The relay logs its start (addresses it listens on, the version and its limits), a summary of counts every 5 minutes, and the start and end of every pressure period. It does not log client addresses or session IDs. `RELAY_LOG_LEVEL=debug` enables debug logging; use it only while diagnosing a problem.

## Upgrade

1. Build (or load) the new image under a new tag, for example `1.1.0`. Keep the old image on the server.
2. Set `RELAY_IMAGE_TAG=1.1.0` in `.env`.
3. Recreate only this project:

   ```sh
   cd /opt/lanfolk-relay
   sudo docker compose -p lanfolk-relay up -d
   ```

Clients lose the relay for a few seconds, re-register automatically and keep their direct paths. Do not use `docker system prune` or other bulk cleanup commands on a server that runs other containers.

## Roll back

Set `RELAY_IMAGE_TAG` back to the previous tag and run the same `docker compose -p lanfolk-relay up -d`. To remove the relay completely:

```sh
cd /opt/lanfolk-relay
sudo docker compose -p lanfolk-relay down
sudo docker image rm lanfolk-relay:1.0.0     # optional, removes only this image
```

Remove the firewall rules you added (for example `sudo ufw delete allow 3478/udp`) if you no longer need them.

## Using your relay from Lanfolk

There are two ways. Both only need the address and ports; players need no other change.

- **Settings, custom relays (per user).** In Lanfolk open Settings and add a relay with your host name or IP address, the UDP port (3478) and the TCP port (443, or `0` for none). A client registers at up to three relays at once and uses the one with the lowest round trip time. Every player who should use this relay must add it, because two players can only meet at a relay where both are registered.

Use a DNS name rather than an IP address where you can: you can then move the relay to another server by changing the record, and clients pick up the new address within about five minutes. If the name is on Cloudflare, set the record to "DNS only" (gray cloud). Cloudflare's proxy only carries web traffic, so a proxied record makes the relay unreachable.
- **The network owner's relay list (per network).** The owner of a private network can attach up to three relays to the network. The list is signed by the owner and sent to every member, so members do not have to add anything. Use this for a group of friends.

Custom relays are used in addition to the default relay that is built into the player's copy of Lanfolk. Traffic stays end-to-end encrypted whichever relay carries it.

## Security notes

- **The relay cannot read game traffic.** Peers encrypt everything with keys the relay never has (WireGuard for game packets, XChaCha20-Poly1305 for control messages). The relay forwards opaque frames.
- **What the relay sees:** the IP address and port of every client that contacts it, the 16-byte session IDs, packet sizes and timing, and how long a session lasts. A session ID is derived from a secret that only the two friends know, so the relay cannot link it to a friend code or a nickname, and it cannot tell which friends play together unless it also watches the network.
- **What the relay cannot do:** it forwards a frame only between the two clients registered under the same session ID. It is not an open proxy and cannot reach any other destination. It can drop or delay packets, which is a loss of service, not a break of confidentiality.
- **Abuse limits:** per-session and per-IP limits, a limit on sessions, a rate limit on control messages, and an idle timeout (section 4.5 of [PROTOCOL.md](PROTOCOL.md)). The relay has no authentication: anybody who can reach it can use it within these limits. Run it for a private group on a firewall-restricted address if you need to control who uses it.
- **Keep the host patched** and keep the metrics endpoint on localhost. Do not store SSH keys, passwords or provider credentials in the deployment folder.
- **Reporting a vulnerability:** see [SECURITY.md](../SECURITY.md).
