# Human test procedures

Automated tests (`go test -race ./...`) catch regressions. This page lists the tests that need real internet lines, two real machines, administrator rights or people. Results go into [MEASUREMENTS.md](MEASUREMENTS.md) so that the project's decisions rest on measurements (see [DECISIONS.md](DECISIONS.md), D-001).

General rules:

- Never write IP addresses, the relay's host name or friend codes into the repository, an issue or a chat. Rows in `MEASUREMENTS.md` have none.
- Record the date, the Windows version and the Lanfolk version with every result.
- Build the tools from the repository root with `go build ./cmd/natprobe ./cmd/lanprobe` or use the ones in the release package.

## 1. NAT behavior on each ISP line (`natprobe`)

Goal: learn how each line's NAT maps ports, how long a mapping lives, whether IPv6 and the DHT work, and how far the relay is.

Run it **once per internet line** (home fiber, DSL, each mobile operator, a phone hotspot), from a computer on that line. Use the relay you want to measure; for the default relay, use the host name you were given.

```powershell
.\natprobe.exe -relay relay.example.org -relay-tcp-port 443 -isp "Superonline"
```

- `-relay`: the relay host (needed for the relay RTT, the TCP test and the NAT timeout test). The host is never printed.
- `-relay-udp-port` (default 3478) and `-relay-tcp-port` (default 443): use the relay's real ports.
- `-isp`: the label of the line, for example the provider's name, or "Mobile (operator)".
- `-wan-ip`: optional. Enter the WAN IP shown on your router's status page to tell a single NAT from CGNAT. It is used only for the comparison and is not printed in the Markdown row.
- `-timeouts`: idle times to test in seconds (default `15,30,60,90,120`). The full run takes a few minutes because it waits for the longest timeout. Do not use the computer's network for anything else meanwhile.
- `-skip-dht`: skip the DHT test if the line blocks it.
- `-json out.json`: also write the full report. **The JSON contains your public IP. Do not share it unredacted.**

How to record the result:

1. At the end the tool prints a table row after the text "Markdown row for docs/MEASUREMENTS.md (no IPs, no relay host)". It starts with `| ISP | Mapping | ...` followed by one line with your values.
2. Open `docs/MEASUREMENTS.md`, find the section "NAT behavior per line", and fill the matching row (or add a new row below the last one). Keep older rows.
3. Add a short note in "Observations" if something is surprising (for example a mapping that expired at 30 s).
4. Open a pull request or send the row to a maintainer.

Privacy: the row contains no IP address and no relay host. The terminal output above it may contain your public IP ("Public IPv4 (do not share)") and, in the JSON, more. Paste only the Markdown row.

Mapping `easy` means hole punching should work. `hard` (symmetric NAT) means Lanfolk will use a member relay or the shared relay on that line.

## 2. LAN behaviors between two machines (`lanprobe`)

Goal: check that unicast, subnet and global broadcast, multicast, IP-in-payload, the private range and the blocked system ports behave like a LAN, without a game.

You need two Windows machines (A and B) in the same Lanfolk network (or, for a first check, on one physical LAN).

On machine B (the server). Allow the ports it lists in Windows Firewall for the `Lanfolk` adapter:

```powershell
.\lanprobe.exe serve -iface-ip 10.77.0.3
```

`-iface-ip` is B's virtual IP; it makes B join the multicast group only on that interface. It uses TCP 7790 and UDP 7791, 7792, 7793 and 7794.

On machine A:

```powershell
.\lanprobe.exe test -peer 10.77.0.3 -local 10.77.0.2 -json lanprobe-a.json
```

The output lists PASS, FAIL or SKIP per behavior: TCP and UDP unicast, subnet broadcast, global broadcast (bound to the local IP and bound to `0.0.0.0`), multicast (with `IP_MULTICAST_IF` and with the default interface), IP in payload, the private range, and blocked ports. Lines marked informational (sockets bound to `0.0.0.0` or the default multicast interface) depend on the interface metric and are expected to fail without compatibility mode. The exit code is 0 if all core tests pass.

Run it twice: with compatibility mode off and on. Swap the roles of A and B and run again, because broadcast must work in both directions. Paste the Markdown for `MEASUREMENTS.md` that the tool prints, under "LAN behaviors between two machines", and edit the first cell to describe the test (for example "A to B, home fiber to mobile, compat off").

### Latency overhead of the tunnel

Target: at most 1 ms at the median (p50) and 3 ms at p99 over the raw path.

On B run `lanprobe serve` as above. On A, send the same packets at the same time to the tunnel address and the raw address of B:

```powershell
.\lanprobe.exe latency -peer 10.77.0.3 -compare 192.168.1.20 -count 10000 -size 200 -rate 64
```

- `-peer`: B's virtual IP.
- `-compare`: B's raw address on the same physical path (for the direct-path overhead test the two machines are on the same LAN, or B's public address if B has an open port). The raw path must be the path the tunnel uses.
- The defaults send 10,000 packets of 200 bytes at 64 packets per second, about 2.6 minutes.

The tool prints the raw and tunnel statistics and the overhead. It exits with 1 if p50 or p99 is above the target. Paste the numbers under "Tunnel overhead". Run without `-compare` to measure one path alone, for example the relayed path.

Do the test with Hamachi, ZeroTier or another tool on the same lines if you want a benchmark.

## 3. Wintun self-test (`lanprobe wintun-selftest`)

Goal: confirm that Windows delivers broadcast and multicast to and from a Layer 3 Wintun adapter, and see how the adapter metric changes it.

1. Download Wintun from <https://www.wintun.net/> or run `scripts\fetch-wintun.ps1`; copy `wintun.dll` (amd64) next to `lanprobe.exe`.
2. Open an **elevated** PowerShell (Run as administrator).
3. Run:

   ```powershell
   .\lanprobe.exe wintun-selftest
   ```

   It creates a temporary adapter named `LanfolkSelfTest` (10.77.0.2/24, MTU 1380), adds a temporary inbound firewall rule for the executable (use `-no-firewall` to skip it), runs the send tests with the automatic metric, the receive tests, repeats the send tests with metric 1, prints a table and removes the adapter and the rule.
4. Paste the table, together with the line that names the Windows version and the Wintun driver version, under "Wintun LAN behavior" in `MEASUREMENTS.md`.

Exit codes: 0 completed, 1 setup failure, 2 prerequisites missing (not elevated or no `wintun.dll`). Antivirus software may block the adapter creation; allow it for the test.

## 4. Elevated Windows network checks (`winnet`)

Goal: confirm on real Windows that the adapter, the metric, the network profile and the firewall rules are set as designed. Needs an elevated shell and a machine where the Lanfolk service is installed with an active network.

First run the package tests that need elevation (they are skipped otherwise):

```powershell
go test -count=1 ./internal/winnet/
```

Then check the live configuration in an elevated PowerShell:

```powershell
# Adapter exists and is up; address 10.77.0.x/24; MTU 1380
Get-NetAdapter -Name Lanfolk
Get-NetIPAddress -InterfaceAlias Lanfolk -AddressFamily IPv4
Get-NetIPInterface -InterfaceAlias Lanfolk -AddressFamily IPv4 | Format-List InterfaceMetric,AutomaticMetric,NlMtu

# No default route through the adapter: only 10.77.0.0/24 (and its host routes)
Get-NetRoute -InterfaceAlias Lanfolk -AddressFamily IPv4

# Network profile is Private
Get-NetConnectionProfile -InterfaceAlias Lanfolk

# Firewall: one group named Lanfolk with 6 rules
Get-NetFirewallRule -Group Lanfolk | Select-Object Name,Direction,Action,Enabled
(Get-NetFirewallRule -Group Lanfolk).Count
```

Expected:

| Check | Expected |
| --- | --- |
| Adapter | `Lanfolk`, status `Up`, address `10.77.0.x/24`, MTU 1380 |
| Metric | `InterfaceMetric` 9000 and `AutomaticMetric` disabled; 1 while compatibility mode is on; back to 9000 afterwards, including after stopping the service |
| Routes | no `0.0.0.0/0` route on the adapter |
| Profile | `NetworkCategory` is `Private` |
| Firewall | exactly 6 enabled rules in group `Lanfolk`: `Lanfolk-In-Service-UDP`, `Lanfolk-In-Allow-VirtualLAN`, `Lanfolk-In-Block-TCP`, `Lanfolk-In-Block-UDP`, `Lanfolk-Out-Block-TCP`, `Lanfolk-Out-Block-UDP` |

SMB and Remote Desktop must be unreachable from other members. From machine B, with SMB and RDP enabled on machine A for the test:

```powershell
Test-NetConnection 10.77.0.2 -Port 445     # expected: TcpTestSucceeded False
Test-NetConnection 10.77.0.2 -Port 3389    # expected: TcpTestSucceeded False
```

and `lanprobe test` reports "Windows service ports blocked" as PASS. Also check that a game port still works (`lanprobe test` unicast).

After `lanfolk-service.exe uninstall` there must be no `Lanfolk` adapter and no rule in the group:

```powershell
Get-NetAdapter -Name Lanfolk -ErrorAction SilentlyContinue
Get-NetFirewallRule -Group Lanfolk -ErrorAction SilentlyContinue
```

Record the result in the Phase 7 notes of the plan or in `MEASUREMENTS.md`.

## 5. Real-game validation

Goal: confirm that the behavior tests reflect reality. This does not limit the supported games; it checks the tests against actual games.

Pick samples from different behavior classes (see [COMPATIBILITY.md](COMPATIBILITY.md)):

- discovery by subnet or global broadcast;
- discovery by multicast;
- join by IP address only;
- a game that announces its own IP in the payload;
- a peer-to-peer game;
- a game that checks for a private IP.

For each class, run a **2-hour session** between two homes (different ISPs if possible) with two to four players. Use Lanfolk's normal settings first; turn on compatibility mode only if discovery fails and record that.

Record for each session:

- game name and version, class(es) it uses, number of players;
- Windows versions and Lanfolk version;
- path type, ping, jitter, loss and quality at the start, and whenever they changed (a screenshot of the connection panel or the diagnostic report is enough);
- whether discovery found the host, whether joining worked, and whether compatibility mode was needed;
- disconnects or lag spikes with the time, and what the connection panel showed then;
- how in-game ping compares with the measured raw path latency;
- the `lanprobe test` result for the same pair of machines.

If a game fails, find the missing network behavior with `lanprobe test` and Wireshark, and report it. Do not request game-specific rules.

Put the results in the "Reported games" table in `COMPATIBILITY.md` (game, version, classes, result) and the details in the session notes.

## 6. Closed beta checklist

Needs at least **10 users on at least 4 different ISPs**, including one mobile line.

- [ ] Every tester installed the release candidate from the installer, on Windows 10 or 11, without help beyond [SETUP.md](SETUP.md).
- [ ] Every tester ran `natprobe` and recorded a row (section 1).
- [ ] Every pair of testers connected; the path type of each pair is recorded.
- [ ] A network of at least 3 members formed with all members behind NAT, without opening ports.
- [ ] A non-member could not join the network.
- [ ] The owner went offline and the other members kept their connections.
- [ ] A relay outage was tested: stopped relay, traffic continued on direct paths, new connections recovered after the relay returned.
- [ ] A path change was tested (Wi-Fi to wired, router restart); the connection recovered without user action.
- [ ] Tester reports were collected: install problems, antivirus warnings, SmartScreen prompts, and uninstall residue.
- [ ] Small-packet loss: 64 packets per second, 200 bytes, 2 hours (`lanprobe latency -count 460800`), added loss below 0.1 %.
- [ ] No known critical bug remains open.

## 7. Installer test on clean Windows 10 and 11

Use fresh virtual machines (snapshot before the test) for Windows 10 and Windows 11, 64-bit. Nested virtualization is not needed.

Install:

1. Copy `Lanfolk-<version>-setup.exe` into the VM. Verify the SHA-256 against `SHA256SUMS`.
2. Run it with administrator rights (Inno Setup wizard). Check the wizard itself: welcome page text, licence page, options page with "Start Lanfolk when I sign in" (on), "Create a desktop shortcut" (off) and "Add the command-line tools to PATH" (on), no "ready" page, the artwork is sharp at 100 %, 150 % and 200 % display scaling, and the wizard follows the Windows light and dark setting (switch the theme and start it again). Run it with and without each option.
3. Check: files in `C:\Program Files\Lanfolk` (`lanfolk-service.exe`, `lanfolk-cli.exe`, `Lanfolk.exe`, `natprobe.exe`, `lanprobe.exe`, `wintun.dll`, `wintun-LICENSE.txt`, `LICENSE`, `THIRD_PARTY_NOTICES.md`, `unins000.exe`), the Start Menu shortcut `Lanfolk` (directly in the Start menu, no uninstall shortcut), the desktop shortcut only if chosen, the entry in Settings, Apps with the right version, publisher and icon, the service `Lanfolk` running with start type automatic (`Get-Service Lanfolk`), the firewall group (section 4), the value `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Lanfolk` (`"C:\Program Files\Lanfolk\Lanfolk.exe" --minimized`) only if chosen, the folder in the system PATH only if chosen (open a new terminal and run `lanprobe`; it must be found), and that `C:\ProgramData\Lanfolk` exists after the first start.
4. Open the app, see the friend code, change the nickname, create a network. On the last wizard page, "Launch Lanfolk" must start the app as the normal (not elevated) user.
5. Reboot. The service starts by itself; the app starts minimized to the tray if the option was chosen.
6. Run the installer again over the existing installation (upgrade), also while `Lanfolk.exe` is running. Identity and settings are kept, the old service is replaced, and options that are switched off are removed (Run value, PATH entry).
7. Failure path: rename `lanfolk-service.exe` in a copy of the build so that `install` fails, or run the installer on a machine where the service cannot be created. The wizard must show the error text with the retry command, the last page must say that Setup could not finish (no "Launch Lanfolk"), and the exit code must be 3.
8. Silent install and uninstall: `Lanfolk-<version>-setup.exe /VERYSILENT /NORESTART` and `"C:\Program Files\Lanfolk\unins000.exe" /VERYSILENT`. No dialog may appear and the data folder must be kept.
9. If a build of the NSIS installer is available, install it first, then run the Inno Setup installer over it: there must be only one Lanfolk in Apps and one Start menu entry afterwards.

Uninstall:

1. Uninstall from Settings, Apps. Answer **No** to deleting the data folder. Check: no `Lanfolk` service, adapter, firewall rules or shortcut; no `Lanfolk` entry in Apps; no `HKCU\...\Run` value; the Lanfolk folder is gone from the system PATH; `C:\Program Files\Lanfolk` is gone; `C:\ProgramData\Lanfolk` is kept.
2. Install again; the identity (friend code) is unchanged.
3. Uninstall and answer **Yes**; `C:\ProgramData\Lanfolk` is gone.
4. Check for residue: `Get-NetAdapter`, `Get-NetFirewallRule -Group Lanfolk`, `Get-Service Lanfolk`, `reg query HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, and a search of `C:\Program Files` and `C:\ProgramData` for `Lanfolk`.
5. Test an uninstall while `Lanfolk.exe` is running (it must be closed by the uninstaller) and while the service is stopped.

Record the Windows build numbers and every deviation in an issue.
