# Lanfolk setup guide

This guide is for players on Windows 10 and 11 (64-bit). Lanfolk lets you and your friends play LAN games over the internet: everyone gets an address in a private virtual network, and games see each other as if they were on the same local network. You do not need to open ports or change router settings.

> Status: the Lanfolk service and desktop app are still under development, and this guide describes how version 1.0 is meant to work. Names of buttons may differ slightly. If something here does not match the app, please open an issue.

Lanfolk is not a VPN for your internet traffic. Only traffic to the virtual network `10.77.0.0/24` goes through it. It does not share files or printers, and Windows file sharing and Remote Desktop are blocked between members.

## What you need

- Windows 10 or 11, 64-bit.
- Administrator rights for the installation (the installer creates a network adapter and a Windows service).
- Friends who install Lanfolk as well.
- A way to send text to each other (a messaging app).

## Install

1. Download `Lanfolk-<version>-setup.exe` from the project's GitHub Releases page, together with `SHA256SUMS`.
2. Optional but recommended: check the file. In PowerShell:

   ```powershell
   Get-FileHash .\Lanfolk-<version>-setup.exe -Algorithm SHA256
   ```

   The value must match the line in `SHA256SUMS`.
3. Run the installer and accept the administrator prompt. Releases are signed once a signing certificate is available; until then, Windows SmartScreen may show "Windows protected your PC". Choose "More info" and then "Run anyway" only if the checksum matched.
4. Follow the wizard: read the licence, then choose the options below and click **Install**. The wizard follows the Windows light or dark setting.

   | Option | Default | What it does |
   | --- | --- | --- |
   | Start Lanfolk when I sign in | on | Starts the app minimized to the tray when you sign in (`Lanfolk.exe --minimized`). The service always starts with Windows. |
   | Create a desktop shortcut | off | Adds a Lanfolk shortcut to your desktop. |
   | Add the command-line tools to PATH | on | Lets you run `lanfolk-cli`, `lanprobe` and `natprobe` from any terminal. Open a new terminal window after the installation. |

5. The installer copies the program to `C:\Program Files\Lanfolk`, registers the Windows service `Lanfolk` (it starts automatically with Windows), adds Windows Firewall rules (rule group `Lanfolk`) and adds a Start Menu shortcut. On the last page, **Launch Lanfolk** starts the app.

To install without the wizard (for example for a test machine), run `Lanfolk-<version>-setup.exe /VERYSILENT /NORESTART` from an elevated prompt; the options keep their defaults; add for example `/MERGETASKS="!autostart,desktopicon"` to change them (task names `autostart`, `desktopicon`, `addtopath`; a leading `!` switches one off).

Lanfolk stores its data in `C:\ProgramData\Lanfolk`: your identity, friends, networks, settings and logs (in the `logs` folder).

## First start

Open **Lanfolk** from the Start Menu. On the first start:

- Your **identity is created automatically**. It is a key pair that stays on your computer, protected by Windows. There is no account and no password.
- Choose a **nickname**. Friends see it in lists. You can change it later.
- Your **friend code** appears in the app. It starts with `lanfolk1` and is 66 characters long. It is safe to share: it only identifies you, and nobody can join your networks with it.

If the app says it cannot reach the service, see [Troubleshooting](#troubleshooting).

Back up your identity once: in Settings, export a backup and choose a password. If you reinstall Windows without a backup, you get a new identity and your friends must add you again.

## Add friends

Friendship needs a code on **both sides**. One-sided adding gives nobody access.

1. Copy your friend code and send it to your friend.
2. Your friend pastes it into **Add friend**. Spaces, dashes and capital letters are ignored, and a code that was cut or mistyped is rejected immediately.
3. Do the same in the other direction: paste your friend's code into **Add friend** on your side.

The friend is shown as **pending** until the other side has added you, and as **active** afterwards. You see whether a friend is online.

## Create a network

A network is a private group. You can be active in one network at a time.

1. Choose **New network** and give it a name.
2. Add your active friends to it. You are the **owner**: only you can add or remove members.
3. Each member gets a fixed virtual address between `10.77.0.2` and `10.77.0.254` (up to 253 members). Members do not need to be friends with each other.

## Join automatically

When you add a friend to your network, their Lanfolk gets the signed member list and shows the network. They choose it as their active network (if they belong to several), and everyone connects to everyone else automatically when they are online. Members keep working from the latest list if the owner is offline; only membership changes need the owner.

To leave a network, choose **Leave**. If the owner removes you, your connections to the other members close.

## Start a game

1. Make sure everyone is online in the same network and the connection panel shows each member (see below).
2. One player hosts the game and the others look for it on the LAN, or join by IP address. The host's virtual address is shown next to their name.
3. If the game asks which network adapter or IP address to use, pick the one named `Lanfolk` (`10.77.0.x`).

## Read the connection panel

For every member of the active network the panel shows:

| Field | Meaning |
| --- | --- |
| Path | **Direct:** straight between the two computers, the best case. **Member relay:** through another member who can reach both of you. **Shared relay:** through the relay server. **TCP fallback:** through the relay server over TCP, used when UDP is blocked; this has the highest latency. |
| Ping | Latest round trip time and the average over the last 60 seconds. |
| Jitter | How much the round trip time varies over 60 seconds. |
| Loss | Percentage of probe packets that got no answer over 60 seconds. |
| Quality | **Good:** ping up to 60 ms, jitter up to 10 ms and loss up to 0.5 %. **Poor:** ping above 120 ms, jitter above 30 ms or loss above 2 %. **Fair:** everything in between. |
| Relay details | The nickname of the relaying member, or the name of the relay. |

A connection starts through the relay and moves to a direct path as soon as one works. The game keeps running during the change. Relayed paths add the distance to the relay server twice, so ping through the shared relay is usually higher than a direct path. If the path stays on **Shared relay** and the ping is high, one of the players may be behind a strict router; a relay closer to you helps (see [SELF_HOST.md](SELF_HOST.md)).

## Settings

| Setting | What it does |
| --- | --- |
| Nickname | The name your friends see. |
| **Relay only** | Never use direct paths. Other members then see only the relay's address, not your home address. Ping may increase. |
| **Compatibility mode** | Lowers the virtual adapter's priority number so Windows sends broadcasts and multicasts through Lanfolk. Turn it on if a game does not find others (see [Troubleshooting](#troubleshooting)). While it is on, local broadcasts may also reach Lanfolk instead of your home network. Lanfolk restores the normal setting when you turn it off or the service stops. |
| **Manual port** | A fixed UDP port that you forwarded in your router yourself. Optional; it can improve direct connections. `0` means automatic. |
| **Automatic port opening** | Ask your router to open a port with UPnP, NAT-PMP or PCP. On by default; harmless if your router does not support it. |
| **DHT fallback** | If no relay can be reached for 30 seconds, look for friends on the BitTorrent network. On by default. It only runs during relay outages. |
| **Custom relays** | Relays you run or trust, in addition to the built-in default relay. See [SELF_HOST.md](SELF_HOST.md). |
| Identity backup | Export or import an encrypted copy of your identity. |
| Diagnostic report | Creates a text report for a bug report. |

Settings apply immediately. The normal flow works with all optional settings off.

## Connection codes: when nothing else works

If the relay is down and the DHT does not work either, two friends can still connect by hand with a **connection code**:

1. One friend chooses **Create connection code** for the other friend and sends the text to them (it starts with `lanfolk-c1`).
2. The other friend pastes it into **Use connection code**.

The code contains encrypted addresses of the sender, readable only by that friend, and **expires after a short time**. Create a new code if it expired. Both friends should create a code for each other if one direction does not connect.

## Uninstall

Use **Settings, Apps, Installed apps, Lanfolk, Uninstall**. It closes the app, stops and removes the service, removes the firewall rules and the virtual adapter, and deletes the program files, shortcuts and the PATH entry.

It then asks whether to delete `C:\ProgramData\Lanfolk` (your identity and friends). The default answer is **No**, so a later reinstall keeps your identity. Answer **Yes** to remove everything. Export an identity backup first if you may want it again.

## Troubleshooting

**The app says the service is not running.**
Open Services (`services.msc`), find **Lanfolk** and start it. The log files are in `C:\ProgramData\Lanfolk\logs`. If the service does not start, reinstall and attach the log to an issue.

**A game does not find the other players.**
Many games find servers by broadcasting on the local network.
1. Check that the member shows as online with a path other than "none" and that you can ping each other: `ping 10.77.0.x` (their virtual address, shown in the app).
2. Turn on **Compatibility mode** in Settings, then restart the game.
3. Try joining by IP address with the host's virtual address instead of searching.
4. If the game lets you choose a network adapter or bind address, choose the `Lanfolk` adapter.
5. Some games only work with online accounts, or announce their own address. Lanfolk does not touch normal internet traffic, so it cannot help with anti-cheat or account checks.

**Windows Firewall or security software blocks the game.**
Lanfolk adds firewall rules for the virtual adapter and marks the network Private. If you use another firewall or antivirus, allow the game on the `Lanfolk` adapter (`10.77.0.0/24`). Do not open Windows file sharing or Remote Desktop to the virtual network; Lanfolk blocks those ports on purpose (TCP 135, 139, 445, 3389, 5985-5986 and UDP 137-138).

**Antivirus blocks the installer or the adapter.**
Lanfolk is open source and uses the signed Wintun driver DLL to create the adapter. If your antivirus quarantines a file, restore it, add an exception for `C:\Program Files\Lanfolk`, and check the SHA-256 checksums of the download against `SHA256SUMS`.

**The relay is unreachable.**
The relay status in the app shows `down`. Some networks block UDP or the relay's port; Lanfolk then tries the TCP fallback automatically, which shows as **TCP fallback**. If both fail: check your internet connection, try another network (for example a phone hotspot), check whether your firewall or the university or office network blocks outgoing connections, and try again later. You can add another relay in Settings, or use connection codes.

**The path stays on "Shared relay" or "TCP fallback", the ping is high.**
Direct connections need the NAT of both players to cooperate. Try again after a minute, or turn on **Automatic port opening** if it is off. A friend with an open port can improve everyone's paths. A relay in your region lowers the ping.

**A friend stays "pending".**
Your friend has not added your code yet. Ask them to paste your friend code, and check that they pasted all 66 characters.

**I lost my identity or got a new computer.**
Import your identity backup in Settings. Without a backup you must create a new identity and add your friends again.

If none of this helps, open an issue on the project page with the diagnostic report from Settings. Do not post your friends' codes or your IP address publicly.
