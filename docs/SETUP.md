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

Open **Lanfolk** from the Start Menu. The first screen asks for one thing: the
**name** your friends will see. You can change it later in Settings.

Your **identity is created automatically**. It is a key pair that stays on
your computer, protected by Windows. There is no account and no password.

Closing the window does not stop Lanfolk: it keeps running in the
notification area (the icons next to the clock). Windows 11 puts new icons
behind the **^** arrow; drag the Lanfolk icon onto the taskbar to keep it
visible. Left-click it to open the window, right-click it for a menu (copy
your IP address, switch network, invite friends, Settings, Quit). The icon
shows the state: blue with a green node when you are connected, an amber ring
while connecting, gray when you are not in a network.

If the app says it cannot reach the service, see [Troubleshooting](#troubleshooting).

Back up your identity once: **Settings, Identity and backup, Back up**. If you
reinstall Windows without a backup, you get a new identity and your friends
must add you again.

## Create a network and invite friends

A network is a private group. You can be active in one network at a time.

1. Choose **Create**, give the network a name and choose **Create network**.
   You are its **owner**: only you can add or remove members.
2. The next screen shows the network's **invite code** (it starts with
   `lanfolk-i1`). Choose **Copy invite** and send it to your friends in any
   chat. Later you find it again in the network's **⋯** menu, **Invite
   friends**.
3. Stay online while your friends join: Lanfolk accepts them automatically.

An invite code works for **7 days** and for any number of people. Treat it
like a password for the network: anyone who has it can join while it is
valid. **Make a new code** on the invite screen stops the old code; people who
already joined stay members. To remove someone, open the member's menu (**⋯**
or right-click) and choose **Remove from …**, then make a new code.

Each member gets a fixed virtual address between `10.77.0.2` and
`10.77.0.254` (up to 253 members).

## Join a network

1. Choose **Join network** and paste the invite code (Lanfolk fills it in by
   itself if you just copied it).
2. Choose **Join network**. Until the owner is online the network shows
   "Waiting for … to come online"; Lanfolk keeps trying, so you can close the
   window.
3. When the owner accepts, the network appears and becomes active if you had
   no active network.

Everyone connects to everyone else automatically when they are online.
Members keep working from the latest member list if the owner is offline;
only membership changes need the owner. To leave a network, open its **⋯**
menu and choose **Leave network**.

## Friends

People you share a network with become friends automatically. The **Friends**
tab shows who is online. From a friend's menu you can add them to one of your
networks with one click (no invite code needed), copy their friend code or
remove them.

You can also add someone by hand with their **friend code** (it starts with
`lanfolk1`): **Friends, Add friend**. Friendship needs both sides: the friend
shows as "Waiting for them to add you" until they add your code, which you
copy with **My friend code**.

## Start a game

1. Make sure everyone is online in the same network: their names have a green
   dot.
2. One player hosts the game and the others look for it on the LAN, or join by
   IP address. Each member's virtual address is shown under their name; click
   your own address at the top to copy it.
3. If the game asks which network adapter or IP address to use, pick the one
   named `Lanfolk` (`10.77.0.x`).

## Read the member list

Each member shows a dot (green online, amber ring connecting, gray offline),
the virtual address, the latest ping and how you reach them. Click a member to
see the details; actions are in the member's **⋯** menu or on right-click.

| Field | Meaning |
| --- | --- |
| Connection | **Direct:** straight between the two computers, the best case. **Member relay:** through another member who can reach both of you. **Shared relay:** through the relay server. **TCP fallback:** through the relay server over TCP, used when UDP is blocked; this has the highest latency. |
| Route | The member or relay the traffic goes through, for relayed connections. |
| Average ping | Round trip time averaged over the last 60 seconds (the list shows the latest one). |
| Jitter | How much the round trip time varies over 60 seconds. |
| Packet loss | Percentage of probe packets that got no answer over 60 seconds. |
| Quality | **Good:** ping up to 60 ms, jitter up to 10 ms and loss up to 0.5 %. **Poor:** ping above 120 ms, jitter above 30 ms or loss above 2 %. **Fair:** everything in between. |

A connection starts through the relay and moves to a direct path as soon as
one works. The game keeps running during the change. Relayed paths add the
distance to the relay server twice, so ping through the shared relay is
usually higher than a direct path. If a member stays on **Shared relay** and
the ping is high, one of the players may be behind a strict router; a relay
closer to you helps (see [SELF_HOST.md](SELF_HOST.md)).

## Settings

Open Settings with the gear in the window or from the tray menu. They open in
their own window.

| Page | Setting | What it does |
| --- | --- | --- |
| General | Nickname | The name your friends see. |
| General | Open at sign-in | Start Lanfolk in the notification area when you sign in. The service itself always starts with Windows. |
| General | **Compatibility mode** | Lowers the virtual adapter's priority number so Windows sends broadcasts and multicasts through Lanfolk. Turn it on if a game does not find others (see [Troubleshooting](#troubleshooting)). While it is on, local broadcasts may also reach Lanfolk instead of your home network. Lanfolk restores the normal setting when you turn it off or the service stops. |
| Connection | **Relay only** | Never use direct paths. Other members then see only the relay's address, not your home address. Ping may increase. |
| Connection | **Automatic port mapping** | Ask your router to open a port with UPnP, NAT-PMP or PCP. On by default; harmless if your router does not support it. |
| Connection | **Public DHT fallback** | If no relay can be reached for 30 seconds, look for members on the BitTorrent network. On by default. It only runs during relay outages. |
| Connection | **Fixed UDP port** | A UDP port that you forwarded in your router yourself. Optional; it can improve direct connections. Empty means automatic. |
| Relays | Your relays | Relays you run or trust, in addition to the built-in relay and the network owner's relays. See [SELF_HOST.md](SELF_HOST.md). |
| Identity and backup | Friend code, backup, restore | Copy your friend code; export or import an encrypted copy of your identity. |
| Troubleshooting | Diagnostic report, connection codes | Create a text report for a bug report; use a connection code (below). |

Settings apply immediately. The normal flow works with all optional settings off.

## Connection codes: when nothing else works

If the relay is down and the DHT does not work either, two friends can still
connect by hand with a **connection code**:

1. One friend opens the other's menu (in the member list or the Friends tab)
   and chooses **Create connection code**, then sends the text (it starts with
   `lanfolk-c1`).
2. The other friend pastes it under **Settings, Troubleshooting, Use a
   connection code**.

The code contains encrypted addresses of the sender, readable only by that
friend, and **expires after 10 minutes**. Create a new code if it expired.
Both friends should create a code for each other if one direction does not
connect.

## Uninstall

Use **Settings, Apps, Installed apps, Lanfolk, Uninstall**. It closes the app, stops and removes the service, removes the firewall rules and the virtual adapter, and deletes the program files, shortcuts and the PATH entry.

It then asks whether to delete `C:\ProgramData\Lanfolk` (your identity and friends). The default answer is **No**, so a later reinstall keeps your identity. Answer **Yes** to remove everything. Export an identity backup first if you may want it again.

## Troubleshooting

**The app says the service is not running.**
Open Services (`services.msc`), find **Lanfolk** and start it. The log files are in `C:\ProgramData\Lanfolk\logs`; the folder is private to the service, so open it as an administrator. If the service does not start, reinstall and attach the log to an issue.

**A game does not find the other players.**
Many games find servers by broadcasting on the local network.
1. Check that the member shows as online with a path other than "none" and that you can ping each other: `ping 10.77.0.x` (their virtual address, shown in the app).
2. Turn on **Compatibility mode** in Settings, then restart the game.
3. Try joining by IP address with the host's virtual address instead of searching.
4. If the game lets you choose a network adapter or bind address, choose the `Lanfolk` adapter.
5. Some games only work with online accounts, or announce their own address. Lanfolk does not touch normal internet traffic, so it cannot help with anti-cheat or account checks.

**Windows Firewall or security software blocks the game.**
Lanfolk adds firewall rules for the virtual adapter and marks the network Private. If you use another firewall or antivirus, allow the game on the `Lanfolk` adapter (`10.77.0.0/24`). Do not open Windows file sharing or Remote Desktop to the virtual network; Lanfolk blocks those ports on purpose (TCP 135, 139, 445, 593, 2869, 3389, 5355, 5357-5358, 5985-5986, 10243 and UDP 137-138, 1900, 3389, 3702, 5355). Programs that run as Windows services are not reachable by members either; **Settings, Troubleshooting** has a switch for the rare game server that runs as a service.

**Antivirus blocks the installer or the adapter.**
Lanfolk's source code is public and it uses the signed Wintun driver DLL to create the adapter. If your antivirus quarantines a file, restore it, add an exception for `C:\Program Files\Lanfolk`, and check the SHA-256 checksums of the download against `SHA256SUMS`.

**The relay is unreachable.**
The main window shows "No relay can be reached" (the relay is Down under Settings, Relays). Some networks block UDP or the relay's port; Lanfolk then tries the TCP fallback automatically, which shows as **TCP fallback**. If both fail: check your internet connection, try another network (for example a phone hotspot), check whether your firewall or the university or office network blocks outgoing connections, and try again later. You can add another relay under Settings, Relays, or use connection codes.

**The path stays on "Shared relay" or "TCP fallback", the ping is high.**
Direct connections need the NAT of both players to cooperate. Try again after a minute, or turn on **Automatic port opening** if it is off. A friend with an open port can improve everyone's paths. A relay in your region lowers the ping.

**A friend stays "pending".**
Your friend has not added your code yet. Ask them to paste your friend code, and check that they pasted all 66 characters. With an invite code this step is not needed: joining a network makes you friends with its owner.

**I lost my identity or got a new computer.**
Import your identity backup in Settings. Without a backup you must create a new identity and add your friends again.

If none of this helps, open an issue on the project page with the diagnostic report from Settings. Do not post your friends' codes or your IP address publicly.
