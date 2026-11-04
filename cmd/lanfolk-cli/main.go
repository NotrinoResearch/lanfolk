// Command lanfolk-cli controls the Lanfolk service from a terminal and is
// the diagnostics tool of the plan's repository layout.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

const usageText = `usage: lanfolk-cli [-json] [-pipe PATH] <command> [arguments]

  status                               service, relays and adapter state
  nickname <name>                      set your display nickname
  friends                              list friends
  friends add <code> [nickname]        add a friend by friend code
  friends remove <key|code>            remove a friend
  networks                             list networks and members
  networks create <name>               create a network (you are the owner)
  networks add <network> <friend>      add an active friend to your network
  networks remove <network> <member>   remove a member from your network
  networks rename <network> <name>     rename your network
  networks relays <network> [host:udp[:tcp] ...]  set your network's relays
  networks activate <network>|none     choose the active network
  networks leave <network>             leave (or, as owner, delete) a network
  invite <network> [reset]             show the invite code of your network
                                       (reset makes a new one and voids the old)
  join <code>                          join a network with an invite code
  join                                 list invites you are using
  join cancel <network>                stop waiting for an invite
  peers [-watch]                       live connection data per member
  settings                             show settings
  settings set key=value ...           relay_only, compat_mode, manual_port,
                                       port_mapping, dht_fallback (true/false)
  settings relays [host:udp[:tcp] ...] set your custom relays
  conncode create <friend|member>      create a 10-minute connection code
  conncode use <code>                  use a code someone sent you
  diag [file]                          write a diagnostic report
  identity export <file>               export a password-protected backup
  identity import <file>               replace your identity with a backup

<network> is a network name, an ID or an ID prefix. <friend> is a nickname,
a friend code or a public key. Passwords are read from LANFOLK_PASSWORD or
asked for on the terminal.`

var jsonOut bool

func main() {
	fs := flag.NewFlagSet("lanfolk-cli", flag.ExitOnError)
	fs.BoolVar(&jsonOut, "json", false, "print raw JSON")
	pipe := fs.String("pipe", ipc.DefaultAddress(), "service pipe or socket")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usageText) }
	fs.Parse(os.Args[1:])
	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if args[0] == "peers" && len(args) > 1 && args[1] == "-watch" {
		cancel()
		ctx = context.Background()
	}
	c, err := ipc.DialAddr(ctx, *pipe)
	if err != nil {
		fail(err)
	}
	defer c.Close()
	if err := run(ctx, c, args); err != nil {
		fail(err)
	}
}

func fail(err error) {
	var e *ipc.Error
	if errors.As(err, &e) {
		fmt.Fprintln(os.Stderr, "error:", e.Message)
	} else {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	os.Exit(1)
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func run(ctx context.Context, c *ipc.Client, a []string) error {
	call := func(method string, params, out any) error { return c.Call(ctx, method, params, out) }
	arg := func(i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	switch a[0] {
	case "status":
		var s ipc.Status
		if err := call(ipc.MethodStatus, nil, &s); err != nil {
			return err
		}
		printStatus(s)
	case "nickname":
		if arg(1) == "" {
			return errors.New("usage: nickname <name>")
		}
		return call(ipc.MethodSetNickname, ipc.NicknameParams{Nickname: strings.Join(a[1:], " ")}, nil)
	case "friends":
		switch arg(1) {
		case "":
			var fs []ipc.Friend
			if err := call(ipc.MethodListFriends, nil, &fs); err != nil {
				return err
			}
			printFriends(fs)
		case "add":
			if arg(2) == "" {
				return errors.New("usage: friends add <code> [nickname]")
			}
			var f ipc.Friend
			if err := call(ipc.MethodAddFriend, ipc.AddFriendParams{Code: arg(2), Nickname: strings.Join(a[3:], " ")}, &f); err != nil {
				return err
			}
			fmt.Println("Friend added. The friendship becomes active when they add your code too.")
		case "remove":
			pk, err := resolveFriend(ctx, c, arg(2))
			if err != nil {
				return err
			}
			return call(ipc.MethodRemoveFriend, ipc.KeyParams{PublicKey: pk}, nil)
		default:
			return errors.New("unknown friends command")
		}
	case "networks":
		return networks(ctx, c, a[1:])
	case "invite":
		return inviteCmd(ctx, c, a[1:])
	case "join":
		return joinCmd(ctx, c, a[1:])
	case "peers":
		if arg(1) == "-watch" {
			return watchPeers(ctx, c)
		}
		var ps []ipc.Peer
		if err := call(ipc.MethodListPeers, nil, &ps); err != nil {
			return err
		}
		printPeers(ps)
	case "settings":
		return settings(ctx, c, a[1:])
	case "conncode":
		switch arg(1) {
		case "create":
			pk, err := resolvePeer(ctx, c, arg(2))
			if err != nil {
				return err
			}
			var r ipc.ConnCodeResult
			if err := call(ipc.MethodCreateConnCode, ipc.KeyParams{PublicKey: pk}, &r); err != nil {
				return err
			}
			fmt.Printf("%s\n(valid until %s)\n", r.Code, r.Expires.Local().Format("15:04"))
		case "use":
			var r ipc.KeyParams
			if err := call(ipc.MethodUseConnCode, ipc.CodeParams{Code: arg(2)}, &r); err != nil {
				return err
			}
			fmt.Println("Code accepted; probing the addresses in it.")
		default:
			return errors.New("usage: conncode create <peer> | conncode use <code>")
		}
	case "diag":
		var r ipc.DiagnosticsResult
		if err := call(ipc.MethodExportDiagnostics, nil, &r); err != nil {
			return err
		}
		if arg(1) == "" {
			fmt.Println(r.Report)
			return nil
		}
		if err := os.WriteFile(arg(1), []byte(r.Report), 0o600); err != nil {
			return err
		}
		fmt.Println("Report written. It contains IP addresses; share it only with people you trust.")
	case "identity":
		return identityCmd(ctx, c, a[1:])
	default:
		return fmt.Errorf("unknown command %q (run without arguments for help)", a[0])
	}
	return nil
}

func printStatus(s ipc.Status) {
	if jsonOut {
		printJSON(s)
		return
	}
	fmt.Printf("Lanfolk %s\n", s.Version)
	fmt.Printf("Nickname:     %s\nFriend code:  %s\n", s.Nickname, s.FriendCode)
	if s.ActiveNetworkName != "" {
		fmt.Printf("Network:      %s (%s)\n", s.ActiveNetworkName, s.VirtualIP)
	} else {
		fmt.Println("Network:      none active")
	}
	adapter := "down"
	if s.AdapterUp {
		adapter = "up"
	}
	if s.AdapterError != "" {
		adapter += " (" + s.AdapterError + ")"
	}
	fmt.Printf("Adapter:      %s\nUDP port:     %d\n", adapter, s.ListenPort)
	for _, r := range s.Relays {
		mode := r.Mode
		if mode == "tcp" {
			mode = "TCP fallback (higher latency)"
		}
		fmt.Printf("Relay:        %s [%s] %s, %.1f ms\n", r.Name, r.Source, mode, r.RTTMs)
	}
	if s.PortMapping != "" {
		fmt.Printf("Port mapping: %s\n", s.PortMapping)
	}
	fmt.Printf("DHT fallback: %s\nRelay only:   %v\nCompat mode:  %v\n", s.DHT, s.RelayOnly, s.CompatMode)
	for _, w := range s.Warnings {
		fmt.Println("Warning:", w)
	}
}

func printFriends(fs []ipc.Friend) {
	if jsonOut {
		printJSON(fs)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NICKNAME\tSTATE\tONLINE\tKEY")
	for _, f := range fs {
		fmt.Fprintf(w, "%s\t%s\t%v\t%s\n", f.Nickname, f.State, f.Online, f.PublicKey[:16])
	}
	w.Flush()
}

func printPeers(ps []ipc.Peer) {
	if jsonOut {
		printJSON(ps)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MEMBER\tIP\tPATH\tVIA\tPING\tAVG\tJITTER\tLOSS\tQUALITY")
	for _, p := range ps {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.1f ms\t%.1f ms\t%.1f ms\t%.1f%%\t%s\n", p.Nickname, p.VirtualIP, p.Path,
			p.RelayDetail, p.PingMs, p.AvgPingMs, p.JitterMs, p.LossPct, p.Quality)
	}
	w.Flush()
}

func watchPeers(ctx context.Context, c *ipc.Client) error {
	if err := c.Subscribe(ctx); err != nil {
		return err
	}
	for m := range c.Events() {
		if m.Event != ipc.EventPeers {
			continue
		}
		var ps []ipc.Peer
		json.Unmarshal(m.Data, &ps)
		fmt.Print("\033[H\033[2J")
		fmt.Println(time.Now().Format("15:04:05"))
		printPeers(ps)
	}
	return errors.New("connection to the service closed")
}

func resolveNetwork(ctx context.Context, c *ipc.Client, ref string) (ipc.Network, error) {
	var ns []ipc.Network
	if err := c.Call(ctx, ipc.MethodListNetworks, nil, &ns); err != nil {
		return ipc.Network{}, err
	}
	var hits []ipc.Network
	for _, n := range ns {
		if strings.EqualFold(n.Name, ref) || n.ID == ref {
			return n, nil
		}
		if ref != "" && strings.HasPrefix(n.ID, strings.ToLower(ref)) {
			hits = append(hits, n)
		}
	}
	if len(hits) == 0 {
		for _, n := range ns {
			if ref != "" && strings.HasPrefix(strings.ToLower(n.Name), strings.ToLower(ref)) {
				hits = append(hits, n)
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return ipc.Network{}, fmt.Errorf("no single network matches %q", ref)
}

func resolveFriend(ctx context.Context, c *ipc.Client, ref string) (string, error) {
	var fs []ipc.Friend
	if err := c.Call(ctx, ipc.MethodListFriends, nil, &fs); err != nil {
		return "", err
	}
	for _, f := range fs {
		if strings.EqualFold(f.Nickname, ref) || f.PublicKey == ref || f.FriendCode == ref {
			return f.PublicKey, nil
		}
	}
	if ref != "" {
		return ref, nil // let the service validate a raw key or code
	}
	return "", errors.New("friend not found")
}

// resolvePeer finds a friend or a member of any network.
func resolvePeer(ctx context.Context, c *ipc.Client, ref string) (string, error) {
	var ns []ipc.Network
	if err := c.Call(ctx, ipc.MethodListNetworks, nil, &ns); err == nil {
		for _, n := range ns {
			for _, m := range n.Members {
				if !m.IsSelf && strings.EqualFold(m.Nickname, ref) {
					return m.PublicKey, nil
				}
			}
		}
	}
	return resolveFriend(ctx, c, ref)
}

func parseRelays(args []string) ([]ipc.Relay, error) {
	var out []ipc.Relay
	for _, s := range args {
		parts := strings.Split(s, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("relay %q must be host:udp or host:udp:tcp", s)
		}
		udp, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("relay %q: bad UDP port", s)
		}
		r := ipc.Relay{Host: parts[0], UDPPort: udp}
		if len(parts) == 3 {
			if r.TCPPort, err = strconv.Atoi(parts[2]); err != nil {
				return nil, fmt.Errorf("relay %q: bad TCP port", s)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func networks(ctx context.Context, c *ipc.Client, a []string) error {
	if len(a) == 0 {
		var ns []ipc.Network
		if err := c.Call(ctx, ipc.MethodListNetworks, nil, &ns); err != nil {
			return err
		}
		if jsonOut {
			printJSON(ns)
			return nil
		}
		for _, n := range ns {
			flags := ""
			if n.Active {
				flags += " [active]"
			}
			if n.IsOwner {
				flags += " [owner]"
			}
			fmt.Printf("%s  %s%s  v%d\n", n.ID[:8], n.Name, flags, n.Version)
			for _, m := range n.Members {
				who := ""
				if m.IsSelf {
					who = " (you)"
				} else if m.IsOwner {
					who = " (owner)"
				}
				fmt.Printf("    %-15s %s%s\n", m.VirtualIP, m.Nickname, who)
			}
		}
		return nil
	}
	need := func(n int, u string) error {
		if len(a) < n {
			return errors.New("usage: networks " + u)
		}
		return nil
	}
	switch a[0] {
	case "create":
		if err := need(2, "create <name>"); err != nil {
			return err
		}
		var n ipc.Network
		if err := c.Call(ctx, ipc.MethodCreateNetwork, ipc.CreateNetworkParams{Name: strings.Join(a[1:], " ")}, &n); err != nil {
			return err
		}
		fmt.Printf("Network %q created (%s).\n", n.Name, n.ID[:8])
		return nil
	case "add", "remove":
		if err := need(3, a[0]+" <network> <member>"); err != nil {
			return err
		}
		n, err := resolveNetwork(ctx, c, a[1])
		if err != nil {
			return err
		}
		pk, err := resolvePeer(ctx, c, a[2])
		if err != nil {
			return err
		}
		m := ipc.MethodAddMember
		if a[0] == "remove" {
			m = ipc.MethodRemoveMember
		}
		return c.Call(ctx, m, ipc.MemberParams{NetworkID: n.ID, PublicKey: pk}, nil)
	case "rename":
		if err := need(3, "rename <network> <name>"); err != nil {
			return err
		}
		n, err := resolveNetwork(ctx, c, a[1])
		if err != nil {
			return err
		}
		return c.Call(ctx, ipc.MethodRenameNetwork, ipc.RenameParams{NetworkID: n.ID, Name: strings.Join(a[2:], " ")}, nil)
	case "relays":
		if err := need(2, "relays <network> [host:udp[:tcp] ...]"); err != nil {
			return err
		}
		n, err := resolveNetwork(ctx, c, a[1])
		if err != nil {
			return err
		}
		rs, err := parseRelays(a[2:])
		if err != nil {
			return err
		}
		return c.Call(ctx, ipc.MethodSetNetworkRelays, ipc.SetRelaysParams{NetworkID: n.ID, Relays: rs}, nil)
	case "activate":
		if err := need(2, "activate <network>|none"); err != nil {
			return err
		}
		id := ""
		if a[1] != "none" {
			n, err := resolveNetwork(ctx, c, a[1])
			if err != nil {
				return err
			}
			id = n.ID
		}
		return c.Call(ctx, ipc.MethodActivateNetwork, ipc.NetworkParams{NetworkID: id}, nil)
	case "leave":
		if err := need(2, "leave <network>"); err != nil {
			return err
		}
		n, err := resolveNetwork(ctx, c, a[1])
		if err != nil {
			return err
		}
		return c.Call(ctx, ipc.MethodLeaveNetwork, ipc.NetworkParams{NetworkID: n.ID}, nil)
	}
	return fmt.Errorf("unknown networks command %q", a[0])
}

func settings(ctx context.Context, c *ipc.Client, a []string) error {
	var s ipc.Settings
	if err := c.Call(ctx, ipc.MethodGetSettings, nil, &s); err != nil {
		return err
	}
	if len(a) == 0 {
		printJSON(s)
		return nil
	}
	switch a[0] {
	case "set":
		for _, kv := range a[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("%q is not key=value", kv)
			}
			b, berr := strconv.ParseBool(v)
			switch k {
			case "relay_only":
				s.RelayOnly = b
			case "compat_mode":
				s.CompatMode = b
			case "port_mapping":
				s.PortMapping = b
			case "dht_fallback":
				s.DHTFallback = b
			case "manual_port":
				n, err := strconv.Atoi(v)
				if err != nil {
					return fmt.Errorf("manual_port must be a number")
				}
				s.ManualPort = n
				berr = nil
			default:
				return fmt.Errorf("unknown setting %q", k)
			}
			if berr != nil {
				return fmt.Errorf("%s must be true or false", k)
			}
		}
	case "relays":
		rs, err := parseRelays(a[1:])
		if err != nil {
			return err
		}
		s.CustomRelays = rs
	default:
		return fmt.Errorf("unknown settings command %q", a[0])
	}
	var out ipc.Settings
	if err := c.Call(ctx, ipc.MethodSetSettings, s, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func identityCmd(ctx context.Context, c *ipc.Client, a []string) error {
	if len(a) < 2 {
		return errors.New("usage: identity export <file> | identity import <file>")
	}
	pw, err := password()
	if err != nil {
		return err
	}
	switch a[0] {
	case "export":
		var r ipc.BackupResult
		if err := c.Call(ctx, ipc.MethodExportIdentity, ipc.PasswordParams{Password: pw}, &r); err != nil {
			return err
		}
		if err := os.WriteFile(a[1], []byte(r.Backup+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println("Backup written. Keep it and its password safe; anyone with both can use your identity.")
	case "import":
		b, err := os.ReadFile(a[1])
		if err != nil {
			return err
		}
		if err := c.Call(ctx, ipc.MethodImportIdentity, ipc.ImportParams{Backup: string(b), Password: pw}, nil); err != nil {
			return err
		}
		fmt.Println("Identity imported. Restart the Lanfolk service to use it.")
	default:
		return fmt.Errorf("unknown identity command %q", a[0])
	}
	return nil
}

func password() (string, error) {
	if p := os.Getenv("LANFOLK_PASSWORD"); p != "" {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "Backup password: ")
	var p string
	if _, err := fmt.Scanln(&p); err != nil {
		return "", errors.New("no password given (set LANFOLK_PASSWORD)")
	}
	return p, nil
}

func inviteCmd(ctx context.Context, c *ipc.Client, a []string) error {
	if len(a) == 0 || (len(a) > 1 && a[1] != "reset") {
		return errors.New("usage: invite <network> [reset]")
	}
	n, err := resolveNetwork(ctx, c, a[0])
	if err != nil {
		return err
	}
	var r ipc.InviteResult
	if err := c.Call(ctx, ipc.MethodCreateInvite, ipc.InviteParams{NetworkID: n.ID, Reset: len(a) > 1}, &r); err != nil {
		return err
	}
	if jsonOut {
		printJSON(r)
		return nil
	}
	fmt.Printf("%s\n(valid until %s; anyone with the code can join %q while you are online)\n",
		r.Code, r.Expires.Local().Format("2006-01-02 15:04"), n.Name)
	return nil
}

func joinCmd(ctx context.Context, c *ipc.Client, a []string) error {
	switch {
	case len(a) == 0:
		var js []ipc.PendingJoin
		if err := c.Call(ctx, ipc.MethodListInvites, nil, &js); err != nil {
			return err
		}
		if jsonOut {
			printJSON(js)
			return nil
		}
		if len(js) == 0 {
			fmt.Println("No invites in progress.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NETWORK\tOWNER\tSTATE\tEXPIRES")
		for _, j := range js {
			name := j.NetworkName
			if name == "" {
				name = j.NetworkID[:8]
			}
			owner := j.OwnerNickname
			if owner == "" {
				owner = j.OwnerPublicKey[:8]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, owner, joinStateText(j), j.Expires.Local().Format("2006-01-02 15:04"))
		}
		return w.Flush()
	case a[0] == "cancel":
		if len(a) < 2 {
			return errors.New("usage: join cancel <network>")
		}
		var js []ipc.PendingJoin
		if err := c.Call(ctx, ipc.MethodListInvites, nil, &js); err != nil {
			return err
		}
		for _, j := range js {
			if j.NetworkID == a[1] || strings.HasPrefix(j.NetworkID, strings.ToLower(a[1])) || strings.EqualFold(j.NetworkName, a[1]) {
				return c.Call(ctx, ipc.MethodCancelInvite, ipc.NetworkParams{NetworkID: j.NetworkID}, nil)
			}
		}
		return fmt.Errorf("no invite in progress for %q", a[1])
	}
	var r ipc.RedeemResult
	if err := c.Call(ctx, ipc.MethodRedeemInvite, ipc.CodeParams{Code: strings.Join(a, "")}, &r); err != nil {
		return err
	}
	if jsonOut {
		printJSON(r)
		return nil
	}
	fmt.Println("Invite accepted. You join as soon as the network owner is online; check progress with: join")
	return nil
}

func joinStateText(j ipc.PendingJoin) string {
	switch j.State {
	case ipc.JoinWaiting:
		return "waiting for the owner to come online"
	case ipc.JoinJoining:
		return "joining"
	case ipc.JoinJoined:
		return "joined"
	case ipc.JoinExpired:
		return "expired"
	}
	return j.State
}
