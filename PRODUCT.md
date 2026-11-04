# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

(The desktop app is a Wails v2 window on Windows 10/11 that renders plain HTML, CSS and JavaScript from `ui/frontend/dist`. It also owns a Windows notification-area (system tray) icon. Design for a desktop window, not a browser page.)

## Users

Friends who want to play LAN-only or LAN-discovery games together over the internet, on Windows. Most are not network experts. The usual moment of use: someone in a voice chat says "let's play", one person creates a network and sends an invite, the others paste it and expect to see each other in the game's LAN browser within a minute. After that the app sits in the tray for hours and is only opened to check who is online or why someone lags.

A smaller group are technical users who host their own relay or debug connections; they need the details, but behind one click, not on the first screen.

## Product Purpose

Lanfolk makes computers on different internet connections behave as if they were on the same local network, so LAN games and tools work without port forwarding. Success: a first-time user installs, creates or joins a network and sees their friend online in under two minutes, without reading documentation, and never has to touch a setting for a normal session.

## Positioning

Open source (Apache-2.0), peer-to-peer with WireGuard encryption, and with no central account server: identities are local keys, networks are owner-signed member lists, and the shared relay only forwards encrypted packets. Anyone can run their own relay. Broadcast and multicast are carried, so LAN discovery works, while Windows file sharing and remote desktop ports stay blocked between members by default.

## Operating Context

- Windows desktop, alongside a game and a voice chat (Discord etc.). The window is opened briefly and closed to the tray; the service keeps running.
- Joining is done by copy and paste of a code through a chat app.
- One network is active at a time; a user may belong to several.
- Diagnostics (path type, ping, jitter, loss, relay state) matter when a game lags.

## Capabilities and Constraints

- The UI is a client of the Lanfolk Windows service over a local named pipe; it stores no data itself. Without the service it can only show that the service is unreachable.
- Concepts: nickname; network (name, owner, members with virtual IPs 10.77.0.x); active network; invite code (one paste to join; the owner must be online when it is used); per-member connection state (direct, member relay, shared relay, TCP fallback; ping, quality good/fair/poor).
- Friends exist in the engine (needed for invites and mutual trust) but are not shown on the main screen; they are listed under Settings, Advanced.
- Settings: nickname, start with Windows, compatibility mode, relay only, port mapping, DHT fallback, manual port, custom relays, identity backup/restore, diagnostics export, about/licenses.
- UI language: English only for now (decision 2026-11-04). Strings should stay easy to extract later.
- Window is compact (Hamachi-like) and lives in the tray: closing hides it; the tray menu can open it, switch the active network, go offline and quit the UI.

## Brand Commitments

- Name: Lanfolk (LAN + folk: the people on your LAN). Lowercase is not the wordmark; the name is written "Lanfolk".
- Personality (decision 2026-11-04): a calm, trustworthy utility that works quietly in the background and fits naturally into Windows. Color is scarce and mostly carries state (online, connecting, offline, quality). Not a gamer-RGB aesthetic.

## Evidence on Hand

- No users, testimonials, benchmarks for marketing, or press exist yet. Measured facts live in `docs/MEASUREMENTS.md` (for example tunnel overhead p50 0.10 ms); do not invent others.
- No logo or brand assets existed before 2026-11-04.

## Product Principles

1. Two minutes to first connection: the first screen asks for at most one thing.
2. Defaults are the product: every setting has a safe default and lives off the main path.
3. Show state, not machinery: "Online, 18 ms, direct" before relay names and ports.
4. Quiet by default: the tray icon and a notification when something changes, nothing more.
5. Honest about security: say plainly who can join and what is blocked.

## Accessibility & Inclusion

Keyboard operable throughout, visible focus, text contrast at WCAG AA, state never conveyed by color alone (dot plus text), respects Windows light/dark theme and reduced motion.
