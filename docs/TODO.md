# Open work

What is left before a 1.0 release, in one place. Tick an item when it is done
and write the result where the item says (usually
[MEASUREMENTS.md](MEASUREMENTS.md)). How to run each test is in
[TESTING.md](TESTING.md).

Last updated: 4 November 2026.

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

- [ ] Repeat the two-home test from 4 November (0.2.1) with 0.3.0 on both
      sides: the log should show one `path selected` line per real change,
      not one every 50 ms (D-030), and renamed members should show their new
      name.
- [ ] Check that Windows shows the notifications (a member coming online, an
      update) on a machine with Focus Assist off and that a full-screen game
      holds them back.
- [ ] Upgrade 0.2.1 to 0.3.0 over the top on a machine with a network and
      friends; everything must still be there.

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

## Product ideas, not planned yet

- [x] A notification when someone joins your network or comes online
      (D-032).
- [ ] Turkish translation of the app and the installer.
- [ ] Joining with an invite while the network owner is offline (D-025).
- [ ] Mica window background on Windows 11.
