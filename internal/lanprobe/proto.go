package lanprobe

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Default ports of the probe server.
var DefaultPorts = Ports{TCP: 7790, Unicast: 7791, Broadcast: 7792, Multicast: 7793, Latency: 7794}

// DefaultGroup is the multicast group used by the multicast test.
const DefaultGroup = "239.255.77.77"

// Ports lists the ports of the probe server.
type Ports struct {
	TCP       int `json:"tcp"`       // control connection and TCP unicast test
	Unicast   int `json:"unicast"`   // UDP unicast echo
	Broadcast int `json:"broadcast"` // UDP broadcast reception
	Multicast int `json:"multicast"` // UDP multicast reception
	Latency   int `json:"latency"`   // UDP latency echo
}

// Message types of the TCP control protocol.
const (
	MsgHello   = "hello"
	MsgHelloOK = "hello-ack"
	MsgData    = "data"
	MsgDataOK  = "data-ack"
	MsgReport  = "report"
	MsgReportR = "report-reply"
	MsgError   = "error"
)

// Msg is one control message. On the wire it is a 4-byte big-endian length
// followed by a JSON document.
type Msg struct {
	Type   string        `json:"type"`
	Token  string        `json:"token,omitempty"`
	Data   []byte        `json:"data,omitempty"`   // MsgData: random block to checksum
	Sum    string        `json:"sum,omitempty"`    // MsgDataOK: hex SHA-256 of Data
	Remote string        `json:"remote,omitempty"` // MsgHelloOK/MsgDataOK: client IP as seen by the server
	Probes []ProbeRecord `json:"probes,omitempty"` // MsgReportR: probes received for the token
	Err    string        `json:"err,omitempty"`
}

// ProbeRecord summarises the probes of one kind that the server received
// for a token.
type ProbeRecord struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Src     string `json:"src"`     // source IP of the received packet
	Claimed string `json:"claimed"` // IP the sender wrote into the payload
	// Match is true when every received packet had a source IP equal to the
	// IP in its payload.
	Match bool `json:"match"`
	// BadSrc is the first source IP that differed from the payload IP.
	BadSrc string `json:"bad_src,omitempty"`
}

// MaxMsg bounds the size of a control message.
const MaxMsg = 1 << 20

// WriteMsg writes m to w as a single Write call.
func WriteMsg(w io.Writer, m *Msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > MaxMsg {
		return errors.New("control message too large")
	}
	out := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(b)), uint32(len(b)))
	out = append(out, b...)
	_, err = w.Write(out)
	return err
}

// ReadMsg reads one control message from r.
func ReadMsg(r io.Reader) (*Msg, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxMsg {
		return nil, fmt.Errorf("invalid control message length %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	m := new(Msg)
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

// NewToken returns a random per-run token.
func NewToken() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const probeMagic = "LFPROBE1"

// Probe is the payload of a test datagram.
type Probe struct {
	Token   string
	Name    string
	Claimed string // the sender's own IP, written into the payload
	Seq     int
}

// Encode returns the datagram payload for p.
func (p Probe) Encode() []byte {
	return []byte(strings.Join([]string{probeMagic, p.Token, p.Name, p.Claimed, strconv.Itoa(p.Seq)}, "|"))
}

// DecodeProbe parses a datagram payload. Anything that is not a probe
// (stray traffic) returns an error.
func DecodeProbe(b []byte) (Probe, error) {
	parts := strings.Split(string(b), "|")
	if len(parts) != 5 || parts[0] != probeMagic || parts[1] == "" || parts[2] == "" {
		return Probe{}, errors.New("not a lanprobe datagram")
	}
	seq, err := strconv.Atoi(parts[4])
	if err != nil {
		return Probe{}, errors.New("bad sequence number")
	}
	return Probe{Token: parts[1], Name: parts[2], Claimed: parts[3], Seq: seq}, nil
}
