// Package tunwrap wraps a WireGuard TUN device so that LAN behaviors work for
// games on top of a Layer 3 tunnel.
//
// WireGuard routes a packet to the peer whose allowed IP (the member's virtual
// IP /32) matches the destination and drops inbound packets whose source is
// not the sender's allowed IP. Broadcast and multicast packets therefore cannot
// cross the tunnel as they are. The wrapper turns each outbound broadcast or
// multicast UDP packet into one unicast LanFrame per connected peer and
// unwraps LanFrames on the way in, rewriting the source to the sender's
// virtual IP. It also enforces the port denylist on unicast traffic in both
// directions, drops IPv6 and spoofed sources, and rate limits the fan-out.
//
// A LanFrame is an IPv4 packet with protocol LanFrameProto (253) addressed
// from the sender's virtual IP to a peer's virtual IP. Its payload is the
// 4 byte LanFrame header 'L' 'F' 0x01 0x00 followed by the complete original
// IPv4 packet.
//
// The unicast path does not allocate. Packets are never delayed: Read returns
// whatever is ready, and every queue is short and bounded; when a queue is
// full the packet is dropped, because a late packet is worse than a lost one.
package tunwrap

import (
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// Config configures a Device.
type Config struct {
	// Self returns this member's virtual IP. It may change when the active
	// network changes. Packets are dropped while it is invalid.
	Self func() netip.Addr
	// Peers returns the virtual IPs of the currently connected peers that get
	// a copy of every broadcast and multicast packet.
	Peers func() []netip.Addr
	// Blocked reports whether traffic of an IP protocol (6 TCP, 17 UDP) to a
	// destination port is denied. It applies to unicast in both directions and
	// to the destination port of broadcast and multicast UDP. Nil allows
	// everything.
	Blocked func(proto uint8, port uint16) bool
	// ServicePort reports whether a local port (6 TCP, 17 UDP) is owned only
	// by system services, which members must not reach. It is asked for
	// inbound UDP (unicast, broadcast and multicast) and for inbound TCP
	// connection attempts (SYN without ACK), so replies to connections this
	// machine opened are never affected. It runs on the packet path and must
	// be fast. Nil allows everything.
	ServicePort func(proto uint8, port uint16) bool
	// Prefix is the virtual network, 10.77.0.0/24. Zero value means that.
	Prefix netip.Prefix
	// MaxCopiesPerSecond is the token bucket rate for fan-out copies.
	// Default 2000.
	MaxCopiesPerSecond int
	// Logger receives diagnostics. Nil discards them.
	Logger *slog.Logger
}

// Stats are cumulative packet counters.
type Stats struct {
	UnicastOut   uint64 // unicast packets passed from the OS towards WireGuard
	UnicastIn    uint64 // unicast packets passed from WireGuard to the OS
	LanFramesOut uint64 // LanFrame copies produced for peers
	LanFramesIn  uint64 // LanFrames unwrapped and delivered to the OS
	Injected     uint64 // captured local broadcasts accepted by InjectLocalBroadcast

	DroppedBlocked   uint64 // destination port on the denylist
	DroppedService   uint64 // inbound to a port owned only by system services
	DroppedIPv6      uint64 // IPv6 packets
	DroppedSpoofed   uint64 // source address not allowed
	DroppedMalformed uint64 // invalid packet or LanFrame
	DroppedRate      uint64 // fan-out copy over the rate limit
	DroppedQueue     uint64 // internal queue full or packet too large for the buffer
	DroppedOther     uint64 // no route, wrong destination, non-UDP broadcast, TTL 0, ...
}

type counters struct {
	unicastOut, unicastIn, lanOut, lanIn, injected        atomic.Uint64
	blocked, ipv6, spoofed, malformed, rate, queue, other atomic.Uint64
	service                                               atomic.Uint64
}

const (
	// minBatch is the smallest BatchSize reported, so that WireGuard passes
	// enough buffers for a broadcast fan-out.
	minBatch = 16
	// readOffset is the headroom used for reads from the inner device. The
	// Linux TUN device needs at least 10 bytes for the virtio header.
	readOffset = 16
	// readBufSize is the size of each inner read buffer (WireGuard's maximum
	// message size).
	readBufSize = 65535
	// numBatches is the number of inner read batches in circulation: one being
	// read, one queued, one being processed.
	numBatches = 3
	// injectQueue is the length of the captured broadcast queue.
	injectQueue = 64
	// maxPending bounds fan-out copies waiting for the next Read.
	maxPending = 1024
	// pbufSize is the capacity of pooled packet buffers.
	pbufSize = 2048

	defaultCopiesPerSecond = 2000
	minBurst               = 16
	closeWait              = time.Second
)

// Device implements tun.Device on top of an inner device.
type Device struct {
	inner tun.Device
	cfg   Config
	log   *slog.Logger

	prefNet, prefMask, prefBcast uint32

	now func() time.Time // replaced in tests

	stats counters

	// Reader goroutine and queues.
	ready      chan *batch
	free       chan *batch
	inject     chan *pbuf
	done       chan struct{}
	readerDone chan struct{}
	closeOnce  sync.Once
	closeErr   error

	// Read state, guarded by rmu.
	rmu         sync.Mutex
	cur         *batch
	curIdx      int
	readErr     error
	pending     []*pbuf
	pendingHead int
	limiter     bucket

	// Write scratch, guarded by wmu.
	wmu      sync.Mutex
	wscratch [][]byte
}

var _ tun.Device = (*Device)(nil)

// batch is one inner Read worth of packets.
type batch struct {
	bufs  [][]byte
	sizes []int
	n     int
	err   error
}

// pbuf is a pooled packet buffer.
type pbuf struct{ b []byte }

var pbufPool = sync.Pool{New: func() any { return &pbuf{b: make([]byte, 0, pbufSize)} }}

// getPbuf returns a pooled buffer of length n.
func getPbuf(n int) *pbuf {
	p := pbufPool.Get().(*pbuf)
	if cap(p.b) < n {
		p.b = make([]byte, n)
	}
	p.b = p.b[:n]
	return p
}

func putPbuf(p *pbuf) {
	if cap(p.b) > 4*pbufSize {
		return // do not keep rare huge buffers
	}
	pbufPool.Put(p)
}

// Wrap returns a Device that wraps inner. The Device owns inner: Close closes
// it.
func Wrap(inner tun.Device, cfg Config) *Device {
	if !cfg.Prefix.IsValid() || !cfg.Prefix.Addr().Unmap().Is4() {
		cfg.Prefix = netip.MustParsePrefix("10.77.0.0/24")
	}
	cfg.Prefix = netip.PrefixFrom(cfg.Prefix.Addr().Unmap(), cfg.Prefix.Bits()).Masked()
	if cfg.MaxCopiesPerSecond <= 0 {
		cfg.MaxCopiesPerSecond = defaultCopiesPerSecond
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	d := &Device{
		inner:      inner,
		cfg:        cfg,
		log:        log,
		now:        time.Now,
		ready:      make(chan *batch, 1),
		free:       make(chan *batch, numBatches),
		inject:     make(chan *pbuf, injectQueue),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		wscratch:   make([][]byte, 0, 64),
	}
	base := cfg.Prefix.Addr().As4()
	d.prefNet = u32(base)
	d.prefMask = ^uint32(0) << (32 - cfg.Prefix.Bits())
	d.prefBcast = d.prefNet | ^d.prefMask
	rate := int64(cfg.MaxCopiesPerSecond)
	d.limiter = bucket{rate: rate, burst: max(rate/10, minBurst) * tokenUnit}

	bs := max(inner.BatchSize(), 1)
	for i := 0; i < numBatches; i++ {
		b := &batch{bufs: make([][]byte, bs), sizes: make([]int, bs)}
		for j := range b.bufs {
			b.bufs[j] = make([]byte, readBufSize)
		}
		d.free <- b
	}
	go d.readLoop()
	return d
}

// readLoop reads the inner device and hands batches to Read. It is a separate
// goroutine so that Read can also wait for injected packets.
func (d *Device) readLoop() {
	defer close(d.readerDone)
	for {
		var b *batch
		select {
		case b = <-d.free:
		case <-d.done:
			return
		}
		b.n, b.err = d.inner.Read(b.bufs, b.sizes, readOffset)
		select {
		case d.ready <- b:
		case <-d.done:
			return
		}
		if b.err != nil {
			return
		}
	}
}

// Stats returns a snapshot of the counters.
func (d *Device) Stats() Stats {
	c := &d.stats
	return Stats{
		UnicastOut:       c.unicastOut.Load(),
		UnicastIn:        c.unicastIn.Load(),
		LanFramesOut:     c.lanOut.Load(),
		LanFramesIn:      c.lanIn.Load(),
		Injected:         c.injected.Load(),
		DroppedBlocked:   c.blocked.Load(),
		DroppedService:   c.service.Load(),
		DroppedIPv6:      c.ipv6.Load(),
		DroppedSpoofed:   c.spoofed.Load(),
		DroppedMalformed: c.malformed.Load(),
		DroppedRate:      c.rate.Load(),
		DroppedQueue:     c.queue.Load(),
		DroppedOther:     c.other.Load(),
	}
}

// File implements tun.Device.
func (d *Device) File() *os.File { return d.inner.File() }

// MTU implements tun.Device.
func (d *Device) MTU() (int, error) { return d.inner.MTU() }

// Name implements tun.Device.
func (d *Device) Name() (string, error) { return d.inner.Name() }

// Events implements tun.Device.
func (d *Device) Events() <-chan tun.Event { return d.inner.Events() }

// BatchSize implements tun.Device. It is at least 16 so that WireGuard passes
// enough buffers for a fan-out.
func (d *Device) BatchSize() int { return max(d.inner.BatchSize(), minBatch) }

// Close implements tun.Device. It closes the inner device, stops the reader
// goroutine and unblocks Read.
func (d *Device) Close() error {
	d.closeOnce.Do(func() {
		close(d.done)
		d.closeErr = d.inner.Close()
		select {
		case <-d.readerDone:
		case <-time.After(closeWait):
			d.log.Warn("tunwrap: inner device read did not stop after close")
		}
	})
	return d.closeErr
}

func (d *Device) closed() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

// InjectLocalBroadcast feeds a global broadcast captured from loopback (capture
// mode) into the outbound path, as if the OS had sent it on the adapter. The
// source is rewritten to Self() and the checksums are recomputed. The packet
// is copied; it never blocks, and it is dropped when the queue is full.
func (d *Device) InjectLocalBroadcast(ipv4 []byte) {
	if d.closed() {
		return
	}
	p := getPbuf(len(ipv4))
	copy(p.b, ipv4)
	select {
	case d.inject <- p:
		d.stats.injected.Add(1)
	default:
		putPbuf(p)
		d.stats.queue.Add(1)
	}
}

func (d *Device) self() ([4]byte, bool) {
	if d.cfg.Self == nil {
		return [4]byte{}, false
	}
	return addr4(d.cfg.Self())
}

func (d *Device) inPrefix(a uint32) bool { return a&d.prefMask == d.prefNet }

// isBroadcast reports whether a is the limited broadcast or the subnet
// broadcast address.
func (d *Device) isBroadcast(a [4]byte) bool {
	v := u32(a)
	return v == 0xffffffff || v == d.prefBcast
}

// validMember reports whether a can be a member's virtual IP: inside the
// prefix and neither its network nor its broadcast address.
func (d *Device) validMember(a [4]byte) bool {
	v := u32(a)
	return d.inPrefix(v) && v != d.prefNet && v != d.prefBcast
}

// portBlocked inspects the destination port of a TCP or UDP packet. It reports
// blocked ports and packets whose transport header is cut short. Non-first
// fragments carry no port and pass; they cannot overwrite the ports of the
// first fragment because a fragment offset is at least 8 bytes.
func (d *Device) portBlocked(pkt []byte, h *ipv4Header) (blocked, malformed bool) {
	if h.proto != protoTCP && h.proto != protoUDP || h.fragOff != 0 {
		return false, false
	}
	if h.total-h.ihl < 4 {
		return false, true
	}
	if d.cfg.Blocked == nil {
		return false, false
	}
	port := uint16(pkt[h.ihl+2])<<8 | uint16(pkt[h.ihl+3])
	return d.cfg.Blocked(h.proto, port), false
}

// toService reports whether an inbound packet would reach a local port that
// only system services own: a UDP datagram, or a TCP connection attempt.
func (d *Device) toService(pkt []byte, h *ipv4Header) bool {
	if d.cfg.ServicePort == nil || h.fragOff != 0 {
		return false
	}
	switch h.proto {
	case protoUDP:
	case protoTCP:
		if h.total-h.ihl < 14 {
			return false
		}
		if flags := pkt[h.ihl+13]; flags&tcpSYN == 0 || flags&tcpACK != 0 {
			return false
		}
	default:
		return false
	}
	port := uint16(pkt[h.ihl+2])<<8 | uint16(pkt[h.ihl+3])
	return d.cfg.ServicePort(h.proto, port)
}

// TCP header flags.
const (
	tcpSYN = 0x02
	tcpACK = 0x10
)

// ---- Outbound: Read ----

// emitter collects the packets of one Read call.
type emitter struct {
	d      *Device
	bufs   [][]byte
	sizes  []int
	offset int
	n      int
}

// Read implements tun.Device. It returns packets the OS sent into the adapter
// (after filtering) and LanFrames for broadcast and multicast. It blocks until
// at least one packet is available and never waits for more once it has one.
func (d *Device) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(bufs) == 0 {
		return 0, nil
	}
	if d.closed() {
		return 0, os.ErrClosed
	}
	d.rmu.Lock()
	defer d.rmu.Unlock()
	em := emitter{d: d, bufs: bufs, sizes: sizes, offset: offset}
	for {
		d.drainPending(&em)
		if em.n == len(bufs) {
			return em.n, nil
		}
		if d.cur != nil {
			d.processCur(&em)
			if em.n == len(bufs) {
				return em.n, nil
			}
		}
		if em.n > 0 {
			// We have something to return: only take what is ready now.
			select {
			case p := <-d.inject:
				d.handleInjected(p, &em)
			case b := <-d.ready:
				d.setCur(b)
			default:
				return em.n, nil
			}
			continue
		}
		if d.readErr != nil {
			return 0, d.readErr
		}
		select {
		case <-d.done:
			return 0, os.ErrClosed
		case p := <-d.inject:
			d.handleInjected(p, &em)
		case b := <-d.ready:
			d.setCur(b)
		}
	}
}

func (d *Device) setCur(b *batch) {
	d.cur, d.curIdx = b, 0
	if b.err != nil {
		d.readErr = b.err
	}
}

func (d *Device) processCur(em *emitter) {
	b := d.cur
	for d.curIdx < b.n && em.n < len(em.bufs) {
		i := d.curIdx
		d.curIdx++
		size := min(b.sizes[i], len(b.bufs[i])-readOffset)
		d.outbound(b.bufs[i][readOffset:readOffset+size], em)
	}
	if d.curIdx >= b.n {
		d.cur = nil
		d.free <- b // never blocks: capacity equals the number of batches
	}
}

func (d *Device) drainPending(em *emitter) {
	for d.pendingHead < len(d.pending) && em.n < len(em.bufs) {
		p := d.pending[d.pendingHead]
		d.pending[d.pendingHead] = nil
		d.pendingHead++
		if len(p.b) <= len(em.bufs[em.n])-em.offset {
			em.sizes[em.n] = copy(em.bufs[em.n][em.offset:], p.b)
			em.n++
		} else {
			d.stats.queue.Add(1)
		}
		putPbuf(p)
	}
	if d.pendingHead >= len(d.pending) {
		d.pending = d.pending[:0]
		d.pendingHead = 0
	}
}

// putUnicast copies an accepted unicast packet into the next output buffer.
func (em *emitter) putUnicast(pkt []byte) {
	if em.n >= len(em.bufs) || len(pkt) > len(em.bufs[em.n])-em.offset {
		em.d.stats.queue.Add(1)
		return
	}
	em.sizes[em.n] = copy(em.bufs[em.n][em.offset:], pkt)
	em.n++
	em.d.stats.unicastOut.Add(1)
}

// putFrame builds a LanFrame for peer, directly in the next output buffer if
// there is one and in the pending queue otherwise.
func (em *emitter) putFrame(self, peer [4]byte, inner []byte) {
	d := em.d
	need := lanOverhead + len(inner)
	if need > maxIPv4Total {
		d.stats.queue.Add(1)
		return
	}
	if em.n < len(em.bufs) {
		dst := em.bufs[em.n]
		if need > len(dst)-em.offset {
			d.stats.queue.Add(1)
			return
		}
		em.sizes[em.n] = writeFrame(dst[em.offset:], self, peer, inner)
		em.n++
		d.stats.lanOut.Add(1)
		return
	}
	if len(d.pending)-d.pendingHead >= maxPending {
		d.stats.queue.Add(1)
		return
	}
	p := getPbuf(need)
	writeFrame(p.b, self, peer, inner)
	d.pending = append(d.pending, p)
	d.stats.lanOut.Add(1)
}

// outbound handles one packet the OS sent into the adapter.
func (d *Device) outbound(pkt []byte, em *emitter) {
	switch ipVersion(pkt) {
	case 4:
	case 6:
		d.stats.ipv6.Add(1)
		return
	default:
		d.stats.malformed.Add(1)
		return
	}
	h, ok := parseIPv4(pkt)
	if !ok {
		d.stats.malformed.Add(1)
		return
	}
	pkt = pkt[:h.total]
	self, ok := d.self()
	if !ok || h.src != self {
		d.stats.spoofed.Add(1)
		return
	}
	switch {
	case d.isBroadcast(h.dst) || isMulticast(h.dst):
		d.fanOut(pkt, &h, self, em)
	case d.validMember(h.dst):
		if h.proto == LanFrameProto {
			d.stats.other.Add(1) // only the wrapper may emit LanFrames
			return
		}
		blocked, malformed := d.portBlocked(pkt, &h)
		if malformed {
			d.stats.malformed.Add(1)
			return
		}
		if blocked {
			d.stats.blocked.Add(1)
			return
		}
		em.putUnicast(pkt)
	default:
		d.stats.other.Add(1)
	}
}

// fanOut sends one LanFrame per connected peer for a broadcast or multicast
// packet whose source is already Self. Only UDP is carried.
func (d *Device) fanOut(pkt []byte, h *ipv4Header, self [4]byte, em *emitter) {
	// Fragments are dropped: rewriting the source of a fragment cannot fix the
	// UDP checksum, and discovery packets are small.
	if h.proto != protoUDP || h.fragmented() {
		d.stats.other.Add(1)
		return
	}
	if h.total-h.ihl < udpHdrLen {
		d.stats.malformed.Add(1)
		return
	}
	if blocked, _ := d.portBlocked(pkt, h); blocked {
		d.stats.blocked.Add(1)
		return
	}
	if d.cfg.Peers == nil {
		return
	}
	now := d.now()
	for _, peer := range d.cfg.Peers() {
		pa, ok := addr4(peer)
		if !ok || pa == self || !d.validMember(pa) {
			continue
		}
		if !d.limiter.take(now) {
			d.stats.rate.Add(1)
			continue
		}
		em.putFrame(self, pa, pkt)
	}
}

// handleInjected processes a captured broadcast.
func (d *Device) handleInjected(p *pbuf, em *emitter) {
	defer putPbuf(p)
	pkt := p.b
	switch ipVersion(pkt) {
	case 4:
	case 6:
		d.stats.ipv6.Add(1)
		return
	default:
		d.stats.malformed.Add(1)
		return
	}
	h, ok := parseIPv4(pkt)
	if !ok {
		d.stats.malformed.Add(1)
		return
	}
	pkt = pkt[:h.total]
	self, ok := d.self()
	if !ok {
		d.stats.spoofed.Add(1)
		return
	}
	if !d.isBroadcast(h.dst) && !isMulticast(h.dst) {
		d.stats.other.Add(1)
		return
	}
	if h.proto != protoUDP || h.fragmented() || h.total-h.ihl < udpHdrLen {
		d.stats.other.Add(1)
		return
	}
	setSource(pkt, &h, self)
	recomputeUDPChecksum(pkt, &h)
	d.fanOut(pkt, &h, self, em)
}

// ---- Inbound: Write ----

// Write implements tun.Device. It filters the decrypted packets from WireGuard,
// unwraps LanFrames and writes the accepted packets to the inner device in one
// call. Dropping a packet is not an error.
func (d *Device) Write(bufs [][]byte, offset int) (int, error) {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	self, selfOK := d.self()
	out := d.wscratch[:0]
	for _, b := range bufs {
		if v := d.inbound(b, offset, self, selfOK); v != nil {
			out = append(out, v)
		}
	}
	var err error
	if len(out) > 0 {
		_, err = d.inner.Write(out, offset)
	}
	clear(out)
	d.wscratch = out[:0]
	if err != nil {
		return 0, err
	}
	return len(bufs), nil
}

// inbound validates one decrypted packet. It returns the buffer to hand to the
// inner device (packet at [offset:]) or nil to drop it.
func (d *Device) inbound(b []byte, offset int, self [4]byte, selfOK bool) []byte {
	if len(b) < offset {
		d.stats.malformed.Add(1)
		return nil
	}
	pkt := b[offset:]
	switch ipVersion(pkt) {
	case 4:
	case 6:
		d.stats.ipv6.Add(1)
		return nil
	default:
		d.stats.malformed.Add(1)
		return nil
	}
	h, ok := parseIPv4(pkt)
	if !ok {
		d.stats.malformed.Add(1)
		return nil
	}
	pkt = pkt[:h.total]
	if !selfOK || h.dst != self {
		d.stats.other.Add(1)
		return nil
	}
	if !d.validMember(h.src) || h.src == self {
		d.stats.spoofed.Add(1)
		return nil
	}
	if h.proto == LanFrameProto {
		return d.inboundLanFrame(b, offset, pkt, &h, self)
	}
	blocked, malformed := d.portBlocked(pkt, &h)
	if malformed {
		d.stats.malformed.Add(1)
		return nil
	}
	if blocked {
		d.stats.blocked.Add(1)
		return nil
	}
	if d.toService(pkt, &h) {
		d.stats.service.Add(1)
		return nil
	}
	d.stats.unicastIn.Add(1)
	return b[:offset+h.total]
}

// inboundLanFrame unwraps a LanFrame in place. pkt is the outer packet, b[offset:]
// trimmed to its total length. The returned buffer is a view of b that starts
// lanOverhead bytes later, so that the inner packet sits at [offset:] again.
func (d *Device) inboundLanFrame(b []byte, offset int, pkt []byte, h *ipv4Header, self [4]byte) []byte {
	if h.ihl != ipv4HdrLen || h.fragmented() || ipChecksum(pkt[:ipv4HdrLen]) != 0 ||
		len(pkt) < lanOverhead || pkt[20] != 'L' || pkt[21] != 'F' || pkt[22] != lanVersion || pkt[23] != 0 {
		d.stats.malformed.Add(1)
		return nil
	}
	inner := pkt[lanOverhead:]
	switch ipVersion(inner) {
	case 4:
	case 6:
		d.stats.ipv6.Add(1)
		return nil
	default:
		d.stats.malformed.Add(1)
		return nil
	}
	ih, ok := parseIPv4(inner)
	if !ok {
		d.stats.malformed.Add(1)
		return nil
	}
	inner = inner[:ih.total]
	if ih.proto != protoUDP || ih.fragmented() {
		d.stats.other.Add(1)
		return nil
	}
	if ih.total-ih.ihl < udpHdrLen {
		d.stats.malformed.Add(1)
		return nil
	}
	if ulen := int(inner[ih.ihl+4])<<8 | int(inner[ih.ihl+5]); ulen < udpHdrLen || ulen > ih.total-ih.ihl {
		d.stats.malformed.Add(1)
		return nil
	}
	if ih.ttl == 0 || !(d.isBroadcast(ih.dst) || isMulticast(ih.dst)) {
		d.stats.other.Add(1)
		return nil
	}
	if blocked, _ := d.portBlocked(inner, &ih); blocked {
		d.stats.blocked.Add(1)
		return nil
	}
	if d.toService(inner, &ih) {
		d.stats.service.Add(1)
		return nil
	}
	setSource(inner, &ih, h.src)
	d.stats.lanIn.Add(1)
	return b[lanOverhead : offset+lanOverhead+ih.total]
}

// ---- Rate limit ----

// tokenUnit is the scale of bucket.tokens: one copy costs tokenUnit. Refill is
// elapsed nanoseconds times the rate, which is exact in integers.
const tokenUnit = int64(time.Second)

// bucket is a token bucket for fan-out copies. It is used under Device.rmu.
type bucket struct {
	rate   int64 // copies per second
	burst  int64 // capacity, scaled by tokenUnit
	tokens int64 // scaled by tokenUnit
	last   time.Time
	primed bool
}

func (b *bucket) take(now time.Time) bool {
	if !b.primed {
		b.primed, b.tokens, b.last = true, b.burst, now
	} else if el := now.Sub(b.last); el > 0 {
		el = min(el, time.Second)
		b.tokens = min(b.tokens+int64(el)*b.rate, b.burst)
		b.last = now
	} else if el < 0 {
		b.last = now
	}
	if b.tokens < tokenUnit {
		return false
	}
	b.tokens -= tokenUnit
	return true
}
