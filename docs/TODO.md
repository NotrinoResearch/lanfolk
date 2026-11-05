# Open work

What is left before a 1.0 release, in one place. Tick an item when it is done
and write the result where the item says (usually
[MEASUREMENTS.md](MEASUREMENTS.md)). How to run each test is in
[TESTING.md](TESTING.md).

Last updated: 5 November 2026.

## Tests that need people, lines or hardware

- [ ] Run `natprobe` on more internet lines: home fiber from another
      provider, DSL, each mobile operator, a phone hotspot. Record one row per
      line (TESTING.md section 1). So far only one fiber line is measured.
- [ ] Connect two physical computers in different homes, on different
      providers. Run `lanprobe test` in both directions, with and without
      compatibility mode, and `lanprobe latency -compare` (section 2). The
      runs so far were between this computer and Windows Sandbox.
- [ ] Two lines behind CGNAT reach each other through the relay. This is the
      open Phase 2 gate in the plan.
- [ ] Restart the relay server and check that the container comes back by
      itself and that clients reconnect (Phase 2 gate).
- [ ] Change the network while connected (Wi-Fi to cable, router restart) and
      check that the connection recovers without user action.
- [ ] Two hours of small packets: `lanprobe latency -count 460800`, added loss
      below 0.1 %.
- [ ] Play real games, one from each behavior class in
      [COMPATIBILITY.md](COMPATIBILITY.md), in 2-hour sessions between two
      homes (section 5).
- [ ] Closed beta with at least 10 people on at least 4 providers, including
      one mobile line (section 6).

## Release 0.3.0

- [x] Repeat the two-home test from 4 November (0.2.1) with 0.3.0 on both
      sides: the log should show one `path selected` line per real change,
      not one every 50 ms (D-030), and renamed members should show their new
      name. Result (4 November, 15:35-15:38): two `path selected` lines at
      start (relay, then direct after 50 ms) and none after, also while the
      friend turned a VPN off; direct 25 ms, jitter 1.7 ms, no loss,
      quality good (with the VPN on: 139 ms, the path was still right). The
      member list shows the new nickname. A longer session is still worth
      doing.
- [ ] Check that Windows shows the notifications (a member coming online, an
      update) on a machine with Focus Assist off and that a full-screen game
      holds them back. On 4 November 0.3.0 showed none: the NOTIFYICONDATAW
      copy from fyne.io/systray has uTimeout and uVersion as two fields, not
      a union, so every later field was 4 bytes off and Shell_NotifyIcon
      succeeded without showing anything. Fixed after 0.3.0 and confirmed on
      screen with the test build; a layout test guards it. Still to check
      with the real triggers in an installed build.
- [ ] Upgrade 0.2.1 to 0.3.0 over the top on a machine with a network and
      friends; everything must still be there.

## Release 0.4.0

- [x] Upgrade 0.3.1 to 0.4.0 over the top (4 November): identity, friend
      code and network kept; the CLI and app connect to the new control pipe
      as the installing user; the data folder is no longer readable for a
      normal user; the diagnostic report hides the friend code and shortens
      public addresses; the Troubleshooting switch for Windows services shows
      Off.
- [x] The shared relay runs 0.2.0 (D-033); a 0.3.1 client registers over
      UDP as before.
- [ ] Two homes with 0.4.0 on both sides: a game works (Minecraft Java LAN,
      a broadcast game), TCP 7680 (a Windows service) is unreachable while a
      desktop listener is reachable, and `Tunnel.DroppedService` counts the
      attempt (docs/TESTING.md).
- [ ] Remove a member who joined with an invite: the old code stops working
      and, after about 90 s, the friendship that only the invite created is
      gone on both sides.

## Release 0.4.1

- [ ] Ship D-034 (captured broadcasts get a fresh UDP checksum), D-035
      (Relay only holds for both directions; WireGuard does not roam) and
      D-036 (a peer's candidates restart failed direct paths).
- [x] Test 0.4.1 ourselves: install the release build over 0.4.0 on both
      lab machines, run the two-home matrix with the installed builds.
      Result (5 November): both upgrades kept everything; 32 of 32 joined
      with no retry, lanprobe core 32 of 32 both ways (MEASUREMENTS.md).
- [x] Finish the two-home matrix (host side x capture/compat on each side x
      Relay only placement, lanprobe both ways and Commandos 2) and record
      it in MEASUREMENTS.md. Result (5 November): 32 of 32 joined, lanprobe
      core 32 of 32 in both directions.
- [x] Find why the path stayed on the relay for over 2 minutes in two matrix
      runs after compatibility mode and Relay only had been switched
      (MEASUREMENTS.md, two-home matrix). Cause and fix: D-036.
- [ ] Diagnostic report: detect Windows Firewall block rules for programs
      (a "Cancel" on the firewall prompt creates them, and they win over
      Lanfolk's allow rule) and tell the user how to remove them.
- [ ] Capture mode does not forward multicast sent to the default
      interface (lanprobe "Mcast (default)" fails on the capture side).
      Decide whether capture should cover it like global broadcast.

## Release 0.5.0: connection resilience

The plan, targets and order are in [RESILIENCE_PLAN.md](RESILIENCE_PLAN.md).

- [ ] Phase 1: impairment hooks for engine tests, gap report in `lanprobe
      latency`, two-machine scenario runner, baseline in MEASUREMENTS.md.
- [ ] Phase 2: compatibility mode without a tunnel restart (2.1),
      make-before-break (2.3), faster return after a short blip (2.4);
      decide adaptive failure detection (2.2) and redundant sending (2.5).
- [ ] Phase 3: field validation between two homes, including a manual
      network switch during a game.

## Installer

- [ ] Install, upgrade and uninstall on a clean Windows 10 (64-bit). Only
      Windows 11 is tested.
- [ ] Walk through the interactive wizard page by page and check how it looks
      in light and dark mode. Only the first page was looked at; the rest ran
      silently.
- [ ] Uninstall and answer Yes to deleting the data; check that
      `C:\ProgramData\Lanfolk` is gone.
- [ ] Restart a clean machine after installing and check that the service and
      the app start by themselves. (Windows Sandbox cannot restart.)
- [ ] Upgrade a machine that still has the old 0.1.0 NSIS install and check
      that the empty program folder is now removed on uninstall (fixed with
      `RemoveDir`, not tested again).

## Before publishing a stable release

- [ ] Buy a code signing certificate and sign the installer and the
      executables. Until then Windows SmartScreen warns about the download.
- [ ] Have a lawyer review the license choice, [LICENSING.md](../LICENSING.md)
      and the [CLA](../CLA.md).
- [x] Add a contact address for commercial license requests to
      LICENSING.md (info@notrino.com).
- [ ] Decide whether to register "Lanfolk" as a trademark.
- [ ] Move the relay to its own server with DDoS protection (for example
      OVH), away from other services, and point
      `lanfolk-relay1.notrino.com` at it (D-029). No new release is needed.
- [ ] Turn CI back on (it is manual-only now, D-002).
- [ ] Add a second relay closer to players in Turkey. From the measured line
      the current relay is about 70 ms away, so two Turkish players on a
      relayed path see about 140 ms.

## Known issues

- [ ] On Linux the relay load test delivers about 96.4 % of packets at every
      load, while Windows delivers 100 %. Probably the test harness, not the
      relay; not confirmed.
- [ ] Network impairment tests with added delay (netem) need a Linux host;
      WSL2 does not support netem.
- [ ] A joiner whose invite was dropped (the owner removed a member, D-033)
      waits forever without an error. The owner should answer with "invite
      no longer valid" and the app should say so.
- [ ] `lanfolk-cli settings set` has no key for "Let members reach Windows
      services" (`allow_services`); only the app and the pipe can set it.
- [ ] Members that also share a physical LAN receive a global broadcast twice
      (over the LAN and through Lanfolk). Most games answer the first copy, so
      they may connect over the physical LAN. Consider a hint in the
      diagnostic report.
- [ ] `lanprobe test` reports "Global bcast (0.0.0.0)" as informational. Since
      D-034 it should pass in capture mode; make it a core check once the
      two-home matrix confirms it.

## Product ideas, not planned yet

- [x] A notification when someone joins your network or comes online
      (D-032).
- [ ] Turkish translation of the app and the installer.
- [ ] Joining with an invite while the network owner is offline (D-025).
- [ ] Mica window background on Windows 11.
