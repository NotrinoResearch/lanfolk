// Package ipc is the local control protocol between the Lanfolk service
// (running as LocalSystem) and its clients: the desktop UI and lanfolk-cli.
//
// Transport: a named pipe on Windows (\\.\pipe\lanfolk), a Unix socket
// elsewhere. Framing: one JSON object per line.
//
//	request:  {"id":1,"method":"friends.add","params":{"code":"lanfolk1..."}}
//	response: {"id":1,"result":{...}}  or  {"id":1,"error":{"code":"invalid","message":"..."}}
//	event:    {"event":"peers","data":[...]}   (after a "subscribe" call)
//
// Only the methods listed below exist; parameters are validated by the
// service. See docs/PROTOCOL.md, section "Local control protocol".
package ipc

import "time"

// Methods.
const (
	MethodStatus            = "status"
	MethodSubscribe         = "subscribe"
	MethodSetNickname       = "identity.setNickname"
	MethodExportIdentity    = "identity.export"
	MethodImportIdentity    = "identity.import"
	MethodListFriends       = "friends.list"
	MethodAddFriend         = "friends.add"
	MethodRemoveFriend      = "friends.remove"
	MethodListNetworks      = "networks.list"
	MethodCreateNetwork     = "networks.create"
	MethodAddMember         = "networks.addMember"
	MethodRemoveMember      = "networks.removeMember"
	MethodRenameNetwork     = "networks.rename"
	MethodSetNetworkRelays  = "networks.setRelays"
	MethodActivateNetwork   = "networks.activate"
	MethodLeaveNetwork      = "networks.leave"
	MethodListPeers         = "peers.list"
	MethodGetSettings       = "settings.get"
	MethodSetSettings       = "settings.set"
	MethodCreateConnCode    = "conncode.create"
	MethodUseConnCode       = "conncode.use"
	MethodExportDiagnostics = "diagnostics.export"
	MethodCreateInvite      = "invites.create"
	MethodRedeemInvite      = "invites.redeem"
	MethodListInvites       = "invites.list"
	MethodCancelInvite      = "invites.cancel"
)

// Events pushed to subscribed clients.
const (
	EventStatus   = "status"   // Status, on change
	EventPeers    = "peers"    // []Peer, every second while a network is active
	EventFriends  = "friends"  // []Friend, on change
	EventNetworks = "networks" // []Network, on change
	EventInvites  = "invites"  // []PendingJoin, on change
)

// Error codes.
const (
	CodeInvalid      = "invalid"      // bad parameters
	CodeNotFound     = "not_found"    // unknown friend, network or member
	CodeForbidden    = "forbidden"    // e.g. only the owner can change membership
	CodeUnauthorized = "unauthorized" // pipe client is not the session owner
	CodeInternal     = "internal"
	CodeUnavailable  = "unavailable" // e.g. adapter could not be created
)

// Status describes the service.
type Status struct {
	Version           string        `json:"version"`
	Nickname          string        `json:"nickname"`
	PublicKey         string        `json:"public_key"`
	FriendCode        string        `json:"friend_code"`
	Relays            []RelayStatus `json:"relays"`
	ActiveNetworkID   string        `json:"active_network_id,omitempty"`
	ActiveNetworkName string        `json:"active_network_name,omitempty"`
	VirtualIP         string        `json:"virtual_ip,omitempty"`
	AdapterUp         bool          `json:"adapter_up"`
	AdapterError      string        `json:"adapter_error,omitempty"`
	ListenPort        int           `json:"listen_port"`
	PortMapping       string        `json:"port_mapping,omitempty"` // e.g. "upnp" when a mapping exists
	DHT               string        `json:"dht"`                    // "off", "standby", "running"
	RelayOnly         bool          `json:"relay_only"`
	CompatMode        bool          `json:"compat_mode"`
	Warnings          []string      `json:"warnings,omitempty"`
}

// RelayStatus is one shared relay in the effective list.
type RelayStatus struct {
	Name   string  `json:"name"`   // host:port as configured
	Source string  `json:"source"` // "default", "network" or "custom"
	Mode   string  `json:"mode"`   // "udp", "tcp" or "down"
	RTTMs  float64 `json:"rtt_ms"`
}

// Friend is one entry of the friend list.
type Friend struct {
	PublicKey  string    `json:"public_key"`
	FriendCode string    `json:"friend_code"`
	Nickname   string    `json:"nickname"`
	State      string    `json:"state"` // "pending" until both sides added each other, then "active"
	Online     bool      `json:"online"`
	AddedAt    time.Time `json:"added_at"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
}

// Relay is a relay address.
type Relay struct {
	Host    string `json:"host"`
	UDPPort int    `json:"udp_port"`
	TCPPort int    `json:"tcp_port"`
}

// Member is one member of a network.
type Member struct {
	PublicKey string `json:"public_key"`
	Nickname  string `json:"nickname"`
	VirtualIP string `json:"virtual_ip"`
	IsSelf    bool   `json:"is_self"`
	IsOwner   bool   `json:"is_owner"`
	IsFriend  bool   `json:"is_friend"`
}

// Network is a private network the user belongs to.
type Network struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	OwnerKey string   `json:"owner_key"`
	IsOwner  bool     `json:"is_owner"`
	Active   bool     `json:"active"`
	Version  uint64   `json:"version"`
	Members  []Member `json:"members"`
	Relays   []Relay  `json:"relays,omitempty"`
}

// Peer is the live connection state of one member of the active network.
type Peer struct {
	PublicKey   string  `json:"public_key"`
	Nickname    string  `json:"nickname"`
	VirtualIP   string  `json:"virtual_ip"`
	Online      bool    `json:"online"`
	Path        string  `json:"path"`         // "direct", "member-relay", "shared-relay", "tcp-fallback" or "none"
	RelayDetail string  `json:"relay_detail"` // relaying member's nickname or the relay name
	PingMs      float64 `json:"ping_ms"`      // latest RTT
	AvgPingMs   float64 `json:"avg_ping_ms"`  // 60 s average
	JitterMs    float64 `json:"jitter_ms"`
	LossPct     float64 `json:"loss_pct"`
	Quality     string  `json:"quality"` // "good", "fair", "poor" or "unknown"
	// Version is the member's Lanfolk release from its Hello; empty for
	// releases before 0.3.0 and before the first Hello.
	Version string `json:"version"`
}

// Pending join states.
const (
	JoinWaiting = "waiting" // the invite's owner has not answered yet
	JoinJoining = "joining" // the owner accepted; the member list is on its way
	JoinJoined  = "joined"  // the network is in the list of networks
	JoinExpired = "expired" // the invite ran out before the join completed
)

// PendingJoin is an invite code the user redeemed and its progress. Entries
// stay in the list for a day after they end.
type PendingJoin struct {
	NetworkID      string    `json:"network_id"`
	NetworkName    string    `json:"network_name,omitempty"` // known once the owner accepted
	OwnerPublicKey string    `json:"owner_public_key"`
	OwnerNickname  string    `json:"owner_nickname,omitempty"`
	State          string    `json:"state"` // JoinWaiting, JoinJoining, JoinJoined or JoinExpired
	Expires        time.Time `json:"expires"`
}

// Settings are the user-adjustable options.
type Settings struct {
	RelayOnly    bool    `json:"relay_only"`
	CompatMode   bool    `json:"compat_mode"`
	ManualPort   int     `json:"manual_port"` // 0 = automatic
	PortMapping  bool    `json:"port_mapping"`
	DHTFallback  bool    `json:"dht_fallback"`
	CustomRelays []Relay `json:"custom_relays"`
	DefaultRelay string  `json:"default_relay"` // read-only
	// Desktop app: Windows notifications and the daily update check.
	Notifications bool `json:"notifications"`
	CheckUpdates  bool `json:"check_updates"`
}

// Parameter and result types.
type (
	NicknameParams struct {
		Nickname string `json:"nickname"`
	}
	PasswordParams struct {
		Password string `json:"password"`
	}
	BackupResult struct {
		Backup string `json:"backup"`
	}
	ImportParams struct {
		Backup   string `json:"backup"`
		Password string `json:"password"`
	}
	AddFriendParams struct {
		Code     string `json:"code"`
		Nickname string `json:"nickname,omitempty"`
	}
	KeyParams struct {
		PublicKey string `json:"public_key"`
	}
	CreateNetworkParams struct {
		Name string `json:"name"`
	}
	NetworkParams struct {
		NetworkID string `json:"network_id"`
	}
	MemberParams struct {
		NetworkID string `json:"network_id"`
		PublicKey string `json:"public_key"`
	}
	RenameParams struct {
		NetworkID string `json:"network_id"`
		Name      string `json:"name"`
	}
	SetRelaysParams struct {
		NetworkID string  `json:"network_id"`
		Relays    []Relay `json:"relays"`
	}
	ConnCodeResult struct {
		Code    string    `json:"code"`
		Expires time.Time `json:"expires"`
	}
	CodeParams struct {
		Code string `json:"code"`
	}
	InviteParams struct {
		NetworkID string `json:"network_id"`
		Reset     bool   `json:"reset,omitempty"` // replace the current code with a new one
	}
	InviteResult struct {
		Code    string    `json:"code"`
		Expires time.Time `json:"expires"`
	}
	RedeemResult struct {
		NetworkID      string `json:"network_id"`
		OwnerPublicKey string `json:"owner_public_key"`
		State          string `json:"state"`
	}
	DiagnosticsResult struct {
		Report string `json:"report"`
	}
)
