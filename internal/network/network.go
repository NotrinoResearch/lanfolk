// Package network implements private networks: owner-signed member lists,
// version checks and virtual IP allocation.
package network

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"unicode/utf8"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
)

// Address plan for every private network.
var (
	Prefix    = netip.MustParsePrefix("10.77.0.0/24")
	FirstHost = netip.MustParseAddr("10.77.0.2")
	LastHost  = netip.MustParseAddr("10.77.0.254")
	Broadcast = netip.MustParseAddr("10.77.0.255")
)

// MaxMembers is the number of usable host addresses (.2 to .254).
const MaxMembers = 253

// Limits on display text.
const (
	MaxNameLen     = 64
	MaxNicknameLen = 32
)

var listMagic = []byte("LFML")

const listFormatVersion = 1

var signDomain = []byte("lanfolk/v1/member-list\x00")

// ID identifies a network.
type ID [16]byte

func (id ID) String() string { return hex.EncodeToString(id[:]) }

// ParseID parses the hex form of an ID.
func ParseID(s string) (ID, error) {
	var id ID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, errors.New("invalid network id")
	}
	copy(id[:], b)
	return id, nil
}

// Member is one entry of a member list.
type Member struct {
	PublicKey identity.PublicKey
	Nickname  string
	VirtualIP netip.Addr
}

// List is the owner-signed description of a network.
type List struct {
	ID        ID
	Name      string
	Owner     identity.PublicKey
	Version   uint64
	Members   []Member
	Signature []byte
}

// New creates a network owned by owner. The owner is the first member.
func New(owner *identity.PrivateKey, ownerNick, name string) (*List, error) {
	if name == "" {
		return nil, errors.New("network name is required")
	}
	l := &List{
		Name:    truncate(name, MaxNameLen),
		Owner:   owner.Public(),
		Version: 1,
		Members: []Member{{PublicKey: owner.Public(), Nickname: truncate(ownerNick, MaxNicknameLen), VirtualIP: FirstHost}},
	}
	if _, err := io.ReadFull(rand.Reader, l.ID[:]); err != nil {
		return nil, err
	}
	l.Sign(owner)
	return l, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Clone returns a deep copy.
func (l *List) Clone() *List {
	c := *l
	c.Members = append([]Member(nil), l.Members...)
	c.Signature = append([]byte(nil), l.Signature...)
	return &c
}

// Member looks up a member by key.
func (l *List) Member(pk identity.PublicKey) (Member, bool) {
	for _, m := range l.Members {
		if m.PublicKey == pk {
			return m, true
		}
	}
	return Member{}, false
}

// MemberByIP looks up a member by virtual IP.
func (l *List) MemberByIP(ip netip.Addr) (Member, bool) {
	for _, m := range l.Members {
		if m.VirtualIP == ip {
			return m, true
		}
	}
	return Member{}, false
}

// Has reports whether pk is a member.
func (l *List) Has(pk identity.PublicKey) bool {
	_, ok := l.Member(pk)
	return ok
}

func (l *List) checkOwner(owner *identity.PrivateKey) error {
	if owner.Public() != l.Owner {
		return errors.New("only the network owner can change membership")
	}
	return nil
}

// AddMember adds pk (or keeps its existing address), bumps the version and
// re-signs the list.
func (l *List) AddMember(owner *identity.PrivateKey, pk identity.PublicKey, nick string) (netip.Addr, error) {
	if err := l.checkOwner(owner); err != nil {
		return netip.Addr{}, err
	}
	if m, ok := l.Member(pk); ok {
		return m.VirtualIP, nil
	}
	ip, err := l.nextFree()
	if err != nil {
		return netip.Addr{}, err
	}
	l.Members = append(l.Members, Member{PublicKey: pk, Nickname: truncate(nick, MaxNicknameLen), VirtualIP: ip})
	l.sortMembers()
	l.Version++
	l.Sign(owner)
	return ip, nil
}

// RemoveMember removes pk, bumps the version and re-signs the list.
func (l *List) RemoveMember(owner *identity.PrivateKey, pk identity.PublicKey) error {
	if err := l.checkOwner(owner); err != nil {
		return err
	}
	if pk == l.Owner {
		return errors.New("the owner cannot be removed")
	}
	out := l.Members[:0]
	found := false
	for _, m := range l.Members {
		if m.PublicKey == pk {
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return errors.New("not a member")
	}
	l.Members = out
	l.Version++
	l.Sign(owner)
	return nil
}

// Rename changes the display name.
func (l *List) Rename(owner *identity.PrivateKey, name string) error {
	if err := l.checkOwner(owner); err != nil {
		return err
	}
	if name == "" {
		return errors.New("network name is required")
	}
	l.Name = truncate(name, MaxNameLen)
	l.Version++
	l.Sign(owner)
	return nil
}

func (l *List) nextFree() (netip.Addr, error) {
	used := make(map[netip.Addr]bool, len(l.Members))
	for _, m := range l.Members {
		used[m.VirtualIP] = true
	}
	for ip := FirstHost; ip.Compare(LastHost) <= 0; ip = ip.Next() {
		if !used[ip] {
			return ip, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("network is full (%d members)", MaxMembers)
}

func (l *List) sortMembers() {
	sort.Slice(l.Members, func(i, j int) bool { return l.Members[i].VirtualIP.Less(l.Members[j].VirtualIP) })
}

func (l *List) body() []byte {
	var b bytes.Buffer
	b.Write(listMagic)
	b.WriteByte(listFormatVersion)
	b.Write(l.ID[:])
	b.WriteByte(byte(len(l.Name)))
	b.WriteString(l.Name)
	b.Write(l.Owner[:])
	binary.Write(&b, binary.BigEndian, l.Version)
	binary.Write(&b, binary.BigEndian, uint16(len(l.Members)))
	for _, m := range l.Members {
		b.Write(m.PublicKey[:])
		ip := m.VirtualIP.As4()
		b.Write(ip[:])
		b.WriteByte(byte(len(m.Nickname)))
		b.WriteString(m.Nickname)
	}
	return b.Bytes()
}

// Sign signs the list with the owner key.
func (l *List) Sign(owner *identity.PrivateKey) {
	l.Signature = owner.Sign(append(append([]byte(nil), signDomain...), l.body()...))
}

// Marshal returns the canonical signed encoding.
func (l *List) Marshal() []byte {
	return append(l.body(), l.Signature...)
}

// Verify checks the owner signature and structural rules.
func (l *List) Verify() error {
	if !l.Owner.Verify(append(append([]byte(nil), signDomain...), l.body()...), l.Signature) {
		return errors.New("member list signature is invalid")
	}
	return l.validate()
}

func (l *List) validate() error {
	if len(l.Members) == 0 || len(l.Members) > MaxMembers {
		return errors.New("invalid member count")
	}
	if !l.Has(l.Owner) {
		return errors.New("owner is not a member")
	}
	keys := make(map[identity.PublicKey]bool)
	ips := make(map[netip.Addr]bool)
	for _, m := range l.Members {
		if keys[m.PublicKey] {
			return errors.New("duplicate member key")
		}
		if ips[m.VirtualIP] {
			return errors.New("duplicate virtual IP")
		}
		if !m.VirtualIP.Is4() || m.VirtualIP.Less(FirstHost) || LastHost.Less(m.VirtualIP) {
			return fmt.Errorf("virtual IP %v outside the network range", m.VirtualIP)
		}
		keys[m.PublicKey] = true
		ips[m.VirtualIP] = true
	}
	return nil
}

// Unmarshal decodes and verifies a signed list.
func Unmarshal(b []byte) (*List, error) {
	r := bytes.NewReader(b)
	errShort := errors.New("member list is truncated")
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil || !bytes.Equal(magic, listMagic) {
		return nil, errors.New("not a member list")
	}
	ver, err := r.ReadByte()
	if err != nil || ver != listFormatVersion {
		return nil, errors.New("unsupported member list version")
	}
	l := &List{}
	if _, err := io.ReadFull(r, l.ID[:]); err != nil {
		return nil, errShort
	}
	name, err := readStr(r, MaxNameLen)
	if err != nil {
		return nil, err
	}
	l.Name = name
	if _, err := io.ReadFull(r, l.Owner[:]); err != nil {
		return nil, errShort
	}
	if err := binary.Read(r, binary.BigEndian, &l.Version); err != nil {
		return nil, errShort
	}
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, errShort
	}
	if n == 0 || n > MaxMembers {
		return nil, errors.New("invalid member count")
	}
	for i := 0; i < int(n); i++ {
		var m Member
		if _, err := io.ReadFull(r, m.PublicKey[:]); err != nil {
			return nil, errShort
		}
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return nil, errShort
		}
		m.VirtualIP = netip.AddrFrom4(ip)
		if m.Nickname, err = readStr(r, MaxNicknameLen); err != nil {
			return nil, err
		}
		l.Members = append(l.Members, m)
	}
	l.Signature = make([]byte, 64)
	if _, err := io.ReadFull(r, l.Signature); err != nil {
		return nil, errShort
	}
	if r.Len() != 0 {
		return nil, errors.New("trailing data after member list")
	}
	if err := l.Verify(); err != nil {
		return nil, err
	}
	return l, nil
}

func readStr(r *bytes.Reader, max int) (string, error) {
	n, err := r.ReadByte()
	if err != nil {
		return "", errors.New("member list is truncated")
	}
	if int(n) > max {
		return "", errors.New("text field too long")
	}
	s := make([]byte, n)
	if _, err := io.ReadFull(r, s); err != nil {
		return "", errors.New("member list is truncated")
	}
	if !utf8.Valid(s) {
		return "", errors.New("text field is not UTF-8")
	}
	return string(s), nil
}

// Supersedes reports whether incoming should replace current. Both lists
// must describe the same network and owner; versions never roll back.
func Supersedes(current, incoming *List) (bool, error) {
	if err := incoming.Verify(); err != nil {
		return false, err
	}
	if current == nil {
		return true, nil
	}
	if incoming.ID != current.ID {
		return false, errors.New("member list belongs to another network")
	}
	if incoming.Owner != current.Owner {
		return false, errors.New("member list has a different owner")
	}
	return incoming.Version > current.Version, nil
}
