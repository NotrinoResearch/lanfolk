package portmap

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPort = 41641

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- fake PCP / NAT-PMP gateway ---

type pmpReq struct {
	at       time.Time
	ver      byte
	op       byte
	lifetime uint32
	intPort  uint16
	extPort  uint16
	nonce    [12]byte
	client   netip.Addr
	proto    byte
	raw      []byte
}

type fakePMP struct {
	conn *net.UDPConn
	port int

	mu                sync.Mutex
	reqs              []pmpReq
	supportPCP        bool
	supportPMP        bool
	unsuppAsPMPFormat bool // answer unsupported PCP in NAT-PMP format
	silent            bool
	drop              int // number of leading requests to ignore
	ext               netip.AddrPort
	maxLifetime       uint32
}

func newFakePMP(t *testing.T) *fakePMP {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePMP{
		conn:       c,
		port:       c.LocalAddr().(*net.UDPAddr).Port,
		supportPCP: true,
		supportPMP: true,
		ext:        ap("203.0.113.7:50000"),
	}
	t.Cleanup(func() { c.Close() })
	go f.serve()
	return f
}

func (f *fakePMP) set(fn func(f *fakePMP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakePMP) snapshot() []pmpReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pmpReq(nil), f.reqs...)
}

// maps returns the map requests (PCP or NAT-PMP) with the given version.
func (f *fakePMP) maps(ver byte) []pmpReq {
	var out []pmpReq
	for _, r := range f.snapshot() {
		if r.ver == ver && r.op == 1 {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakePMP) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := f.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		p := append([]byte(nil), buf[:n]...)
		f.mu.Lock()
		resp := f.handle(p)
		f.mu.Unlock()
		if resp != nil {
			f.conn.WriteToUDPAddrPort(resp, from)
		}
	}
}

// handle records p and returns the reply, if any. f.mu is held.
func (f *fakePMP) handle(p []byte) []byte {
	if len(p) < 2 {
		return nil
	}
	r := pmpReq{at: time.Now(), ver: p[0], op: p[1] & 0x7f, raw: p}
	switch {
	case p[0] == 2 && len(p) >= 60:
		r.lifetime = binary.BigEndian.Uint32(p[4:])
		r.client = netip.AddrFrom16([16]byte(p[8:24])).Unmap()
		r.nonce = [12]byte(p[24:36])
		r.proto = p[36]
		r.intPort = binary.BigEndian.Uint16(p[40:])
		r.extPort = binary.BigEndian.Uint16(p[42:])
	case p[0] == 0 && p[1] == 1 && len(p) >= 12:
		r.intPort = binary.BigEndian.Uint16(p[4:])
		r.extPort = binary.BigEndian.Uint16(p[6:])
		r.lifetime = binary.BigEndian.Uint32(p[8:])
	}
	f.reqs = append(f.reqs, r)
	if f.silent {
		return nil
	}
	if f.drop > 0 {
		f.drop--
		return nil
	}
	grant := r.lifetime
	if f.maxLifetime != 0 && grant > f.maxLifetime {
		grant = f.maxLifetime
	}
	extPort := f.ext.Port()
	if r.lifetime == 0 || extPort == 0 {
		extPort = r.extPort
	}
	switch p[0] {
	case 2:
		if !f.supportPCP {
			if f.unsuppAsPMPFormat {
				return []byte{0, 0x80 | p[1], 0, 1, 0, 0, 0, 1}
			}
			resp := make([]byte, 24)
			resp[0], resp[1], resp[3] = 2, 0x80|p[1], 1
			return resp
		}
		resp := make([]byte, 60)
		resp[0], resp[1] = 2, 0x81
		binary.BigEndian.PutUint32(resp[4:], grant)
		binary.BigEndian.PutUint32(resp[8:], 1)
		copy(resp[24:36], r.nonce[:])
		resp[36] = r.proto
		binary.BigEndian.PutUint16(resp[40:], r.intPort)
		binary.BigEndian.PutUint16(resp[42:], extPort)
		a := f.ext.Addr().As16()
		copy(resp[44:60], a[:])
		return resp
	case 0:
		if !f.supportPMP {
			return nil
		}
		if r.op == 0 {
			resp := make([]byte, 12)
			resp[1] = 128
			binary.BigEndian.PutUint32(resp[4:], 1)
			a := f.ext.Addr().As4()
			copy(resp[8:12], a[:])
			return resp
		}
		resp := make([]byte, 16)
		resp[1] = 129
		binary.BigEndian.PutUint32(resp[4:], 1)
		binary.BigEndian.PutUint16(resp[8:], r.intPort)
		binary.BigEndian.PutUint16(resp[10:], extPort)
		binary.BigEndian.PutUint32(resp[12:], grant)
		return resp
	}
	return nil
}

// --- fake UPnP IGD ---

type soapCall struct {
	action string
	args   map[string]string
	at     time.Time
}

type fakeIGD struct {
	srv *httptest.Server

	mu           sync.Mutex
	calls        []soapCall
	serviceType  string
	extIP        string
	rejectLeases bool // answer error 725 to AddPortMapping with a non-zero lease
}

var soapArg = regexp.MustCompile(`<(New\w+)>([^<]*)</New\w+>`)

const rootDescTmpl = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
<specVersion><major>1</major><minor>0</minor></specVersion>
<device>
<deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
<friendlyName>Fake IGD</friendlyName><UDN>uuid:fake-igd</UDN>
<deviceList><device>
<deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
<friendlyName>WAN</friendlyName><UDN>uuid:fake-wan</UDN>
<deviceList><device>
<deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
<friendlyName>WANConn</friendlyName><UDN>uuid:fake-conn</UDN>
<serviceList><service>
<serviceType>%s</serviceType>
<serviceId>urn:upnp-org:serviceId:WANConn1</serviceId>
<SCPDURL>/scpd.xml</SCPDURL><controlURL>/ctl</controlURL><eventSubURL>/evt</eventSubURL>
</service></serviceList>
</device></deviceList>
</device></deviceList>
</device></root>`

func newFakeIGD(t *testing.T, serviceType string) *fakeIGD {
	t.Helper()
	f := &fakeIGD{serviceType: serviceType, extIP: "203.0.113.9"}
	mux := http.NewServeMux()
	mux.HandleFunc("/rootDesc.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, rootDescTmpl, f.serviceType)
	})
	mux.HandleFunc("/ctl", f.control)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIGD) rootURL() string { return f.srv.URL + "/rootDesc.xml" }

func (f *fakeIGD) control(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	action := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	if i := strings.LastIndex(action, "#"); i >= 0 {
		action = action[i+1:]
	}
	args := map[string]string{}
	for _, m := range soapArg.FindAllStringSubmatch(string(body), -1) {
		args[m[1]] = m[2]
	}
	f.mu.Lock()
	f.calls = append(f.calls, soapCall{action, args, time.Now()})
	st, ext, reject := f.serviceType, f.extIP, f.rejectLeases
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/xml")
	const env = `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>%s</s:Body></s:Envelope>`
	if action == "AddPortMapping" && reject && args["NewLeaseDuration"] != "0" {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, env, `<s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>725</errorCode><errorDescription>OnlyPermanentLeasesSupported</errorDescription></UPnPError></detail></s:Fault>`)
		return
	}
	out := ""
	if action == "GetExternalIPAddress" {
		out = "<NewExternalIPAddress>" + ext + "</NewExternalIPAddress>"
	}
	fmt.Fprintf(w, env, fmt.Sprintf(`<u:%sResponse xmlns:u="%s">%s</u:%sResponse>`, action, st, out, action))
}

func (f *fakeIGD) snapshot() []soapCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]soapCall(nil), f.calls...)
}

func (f *fakeIGD) count(action string) int {
	n := 0
	for _, c := range f.snapshot() {
		if c.action == action {
			n++
		}
	}
	return n
}

// --- harness ---

type event struct {
	mp Mapping
	ok bool
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newTestMapper returns a Mapper aimed at gw (nil: nothing listening on the
// PCP port) with fast timers. UPnP points at a closed port unless the test
// overrides it, so SSDP is never used.
func newTestMapper(t *testing.T, gw *fakePMP, lifetime time.Duration, logw io.Writer) *Mapper {
	t.Helper()
	if logw == nil {
		logw = io.Discard
	}
	port := 1
	if gw != nil {
		port = gw.port
	}
	m := New(testPort, Options{
		Logger:      slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Gateway:     func() (netip.Addr, error) { return netip.MustParseAddr("127.0.0.1"), nil },
		Lifetime:    lifetime,
		PMPPort:     port,
		UPnPRootURL: "http://127.0.0.1:1/rootDesc.xml",
	})
	m.attemptTimeout = 600 * time.Millisecond
	m.upnpWait = 600 * time.Millisecond
	m.initialRTO = 100 * time.Millisecond
	m.backoffMin = 100 * time.Millisecond
	m.backoffMax = 400 * time.Millisecond
	return m
}

func watch(m *Mapper) <-chan event {
	ch := make(chan event, 32)
	m.OnChange(func(mp Mapping, ok bool) { ch <- event{mp, ok} })
	return ch
}

func recv(t *testing.T, ch <-chan event, what string) event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return event{}
	}
}

// start runs m in the background; stop cancels it and waits for Run to
// return.
func start(t *testing.T, m *Mapper) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}
