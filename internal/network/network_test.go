package network

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
)

func mustKey(t *testing.T) *identity.PrivateKey {
	t.Helper()
	k, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNewNetworkOwnerGetsFirstAddress(t *testing.T) {
	owner := mustKey(t)
	l, err := New(owner, "Ahmet", "Friday games")
	if err != nil {
		t.Fatal(err)
	}
	if l.Version != 1 || l.Owner != owner.Public() {
		t.Fatalf("unexpected header: %+v", l)
	}
	m, ok := l.Member(owner.Public())
	if !ok || m.VirtualIP != netip.MustParseAddr("10.77.0.2") {
		t.Fatalf("owner address = %v", m.VirtualIP)
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestAddRemoveMembersAndAllocation(t *testing.T) {
	owner := mustKey(t)
	l, _ := New(owner, "Ahmet", "net")
	mehmet, veli := mustKey(t), mustKey(t)
	ip1, err := l.AddMember(owner, mehmet.Public(), "Mehmet")
	if err != nil {
		t.Fatal(err)
	}
	ip2, _ := l.AddMember(owner, veli.Public(), "Veli")
	if ip1.String() != "10.77.0.3" || ip2.String() != "10.77.0.4" {
		t.Fatalf("allocation: %v %v", ip1, ip2)
	}
	if l.Version != 3 {
		t.Fatalf("version = %d", l.Version)
	}
	// Adding an existing member keeps the address stable.
	again, err := l.AddMember(owner, mehmet.Public(), "Mehmet")
	if err != nil || again != ip1 {
		t.Fatalf("re-add changed address: %v %v", again, err)
	}
	if err := l.RemoveMember(owner, mehmet.Public()); err != nil {
		t.Fatal(err)
	}
	if l.Has(mehmet.Public()) {
		t.Fatal("removed member still present")
	}
	if err := l.RemoveMember(owner, owner.Public()); err == nil {
		t.Fatal("owner removal accepted")
	}
	// Freed addresses are reused lowest-first.
	k := mustKey(t)
	ip3, _ := l.AddMember(owner, k.Public(), "New")
	if ip3.String() != "10.77.0.3" {
		t.Fatalf("reuse: %v", ip3)
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyOwnerCanModify(t *testing.T) {
	owner := mustKey(t)
	l, _ := New(owner, "Ahmet", "net")
	intruder := mustKey(t)
	if _, err := l.AddMember(intruder, mustKey(t).Public(), "x"); err == nil {
		t.Fatal("non-owner modification accepted")
	}
}

func TestCapacity(t *testing.T) {
	owner := mustKey(t)
	l, _ := New(owner, "o", "big")
	for i := 0; i < MaxMembers-1; i++ {
		if _, err := l.AddMember(owner, mustKey(t).Public(), fmt.Sprintf("m%d", i)); err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
	}
	if len(l.Members) != MaxMembers {
		t.Fatalf("members = %d", len(l.Members))
	}
	if _, err := l.AddMember(owner, mustKey(t).Public(), "overflow"); err == nil {
		t.Fatal("254th member accepted")
	}
	last := l.Members[len(l.Members)-1].VirtualIP
	if last.String() != "10.77.0.254" {
		t.Fatalf("last address %v", last)
	}
	// A full list still fits the encoding and verifies.
	dec, err := Unmarshal(l.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Members) != MaxMembers {
		t.Fatal("decode lost members")
	}
}

func TestMarshalRoundTripAndTamper(t *testing.T) {
	owner := mustKey(t)
	l, _ := New(owner, "Ahmet", "Friday games")
	l.AddMember(owner, mustKey(t).Public(), "Mehmet")
	b := l.Marshal()
	dec, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Name != l.Name || dec.Version != l.Version || len(dec.Members) != 2 || dec.ID != l.ID {
		t.Fatal("round trip mismatch")
	}
	for i := 0; i < len(b); i += 7 {
		c := append([]byte(nil), b...)
		c[i] ^= 0x40
		if _, err := Unmarshal(c); err == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
}

func TestSupersedes(t *testing.T) {
	owner := mustKey(t)
	l, _ := New(owner, "o", "n")
	old := l.Clone()
	l.AddMember(owner, mustKey(t).Public(), "m")
	if ok, err := Supersedes(old, l); !ok || err != nil {
		t.Fatalf("newer list rejected: %v", err)
	}
	if ok, _ := Supersedes(l, old); ok {
		t.Fatal("rollback accepted")
	}
	if ok, _ := Supersedes(l, l.Clone()); ok {
		t.Fatal("same version accepted as newer")
	}
	other, _ := New(owner, "o", "other")
	other.Version = 99
	other.Sign(owner)
	if _, err := Supersedes(l, other); err == nil {
		t.Fatal("list for another network accepted")
	}
	// A list re-signed by someone else for the same ID is rejected.
	forged := l.Clone()
	intruder := mustKey(t)
	forged.Owner = intruder.Public()
	forged.Version = 100
	forged.Sign(intruder)
	if _, err := Supersedes(l, forged); err == nil {
		t.Fatal("owner change accepted")
	}
}

func TestDecodeRejectsDuplicateAddresses(t *testing.T) {
	owner := mustKey(t)
	l, _ := New(owner, "o", "n")
	l.AddMember(owner, mustKey(t).Public(), "a")
	l.Members[1].VirtualIP = l.Members[0].VirtualIP
	l.Sign(owner)
	if _, err := Unmarshal(l.Marshal()); err == nil {
		t.Fatal("duplicate address accepted")
	}
}

func TestIsVirtual(t *testing.T) {
	if !Prefix.Contains(netip.MustParseAddr("10.77.0.9")) {
		t.Fatal("prefix")
	}
	if Broadcast.String() != "10.77.0.255" {
		t.Fatal("broadcast")
	}
}
