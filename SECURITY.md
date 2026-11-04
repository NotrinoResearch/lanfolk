# Security policy

## Reporting a vulnerability

Please report security problems **privately**. Do not open a public issue,
pull request or discussion for a vulnerability.

Use GitHub's private vulnerability reporting: open the repository's
**Security** tab, choose **Advisories**, then **Report a vulnerability**
(<https://github.com/NotrinoResearch/lanfolk/security/advisories/new>). If the
repository moves, the Security tab of the current repository is the place to
report. If you cannot use GitHub, write to <info@notrino.com> with "Lanfolk
security" in the subject.

Please include:

- what you found and which component is affected (client, service, relay,
  installer);
- the version or commit, and your Windows version if relevant;
- steps or a proof of concept that reproduce it;
- what an attacker gains, and what they need to be able to do first.

Do not include other people's friend codes or IP addresses. Do not test
against relays you do not run, and do not run load tests against the default
relay; use your own relay (see [docs/SELF_HOST.md](docs/SELF_HOST.md)).

We aim to acknowledge a report within 7 days and to tell you what we plan to
do within 30 days. We will credit you in the advisory unless you prefer not to
be named. Please give us reasonable time to publish a fix before you disclose
the problem.

## Supported versions

Lanfolk has not had a stable release yet. Until version 1.0, only the latest
commit on the main branch receives fixes. After 1.0, the latest release and
the release before it are supported.

## Scope

In scope:

- the Lanfolk service, command line client and desktop app;
- the packet formats and cryptography in [docs/PROTOCOL.md](docs/PROTOCOL.md);
- the local control protocol and the named pipe `\\.\pipe\lanfolk` (for
  example a way for another local user to control the service);
- the relay (`lanfolk-relay`) and its Docker deployment files;
- the installer and the build and release scripts;
- a bypass of the firewall rules that block Windows file sharing, Remote
  Desktop and similar services between network members;
- impersonation of a friend or member, or joining a network without being
  added by its owner.

Out of scope:

- vulnerabilities in third-party software (WireGuard, Wintun, Windows,
  Docker, games); report those upstream, but tell us if Lanfolk's use of them
  makes the problem worse;
- game cheating, anti-cheat and online account problems;
- denial of service that needs more traffic than the relay's documented rate
  limits already allow to a single client, and load tests against the shared
  relay;
- problems that need administrator rights on the victim's machine;
- the legal or terms-of-service status of any game.

## What the relay can and cannot see

The relay forwards encrypted frames between two clients that registered the
same session ID.

It **can** see:

- the IP address and port of every client that connects to it;
- 16-byte session IDs (derived from a secret that only the two friends know,
  so they do not reveal friend codes or nicknames);
- packet sizes, timing and how long a session lasts;
- its own metrics and counters.

It **cannot**:

- read game traffic or peer control messages (they are encrypted end to end
  with keys the relay never has);
- see friend lists, network names, nicknames or network membership;
- forward traffic to any destination other than the other registered party of
  a session;
- forge traffic that a peer would accept, because peers authenticate every
  packet.

A malicious relay can drop or delay traffic, or refuse service. Players who
want to hide their IP address from other members can use the **Relay only**
setting, so that other members see the relay's address instead of theirs.

The DHT fallback is described in [docs/DECISIONS.md](docs/DECISIONS.md)
(D-013, D-014): DHT nodes can see participants' IP addresses while the
fallback runs, but not the contents of records.

## Releases

Release files are published with SHA-256 checksums. Windows installers and
executables are code-signed once a signing certificate is available; until
then, verify the checksum.
