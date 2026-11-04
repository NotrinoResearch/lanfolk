package winnet

import (
	"sync/atomic"
	"time"
)

// ServiceFilter answers whether a local port is owned only by system
// services (processes in session 0: Windows services, the System process
// with HTTP.sys and SMB). Members of a network must not reach those: SSH or
// database servers, remote access tools, media servers and the like. Programs
// on the user's desktop (games, a game's own server) stay reachable without
// any firewall prompt.
//
// The packet path only does an atomic load and a map lookup. The table of
// port owners is rebuilt in the background when it is older than maxAge and
// traffic asks for it. A port missing from the table is allowed: it is
// either unbound or was opened in the last moment, which is what a game that
// just started looks like.
type ServiceFilter struct {
	table  atomic.Pointer[serviceTable]
	busy   atomic.Bool
	maxAge time.Duration
	load   func() (map[uint32]bool, error)
}

type serviceTable struct {
	at      time.Time
	service map[uint32]bool // key: proto<<16 | port; true: only services own it
}

func serviceKey(proto uint8, port uint16) uint32 { return uint32(proto)<<16 | uint32(port) }

// newServiceFilter builds a filter around a table loader and loads it once.
func newServiceFilter(load func() (map[uint32]bool, error), maxAge time.Duration) *ServiceFilter {
	f := &ServiceFilter{load: load, maxAge: maxAge}
	f.refresh()
	return f
}

// ServicePort reports whether only system services listen on the local port
// (proto 6 TCP, 17 UDP) for the virtual network.
func (f *ServiceFilter) ServicePort(proto uint8, port uint16) bool {
	t := f.table.Load()
	if t == nil || time.Since(t.at) > f.maxAge {
		if f.busy.CompareAndSwap(false, true) {
			go func() {
				defer f.busy.Store(false)
				f.refresh()
			}()
		}
	}
	if t == nil {
		return false
	}
	return t.service[serviceKey(proto, port)]
}

func (f *ServiceFilter) refresh() {
	m, err := f.load()
	if err != nil {
		// Keep the previous table, but try again on the next packet after
		// maxAge rather than at once.
		if old := f.table.Load(); old != nil {
			f.table.Store(&serviceTable{at: time.Now(), service: old.service})
		}
		return
	}
	f.table.Store(&serviceTable{at: time.Now(), service: m})
}

// portOwner is one listening socket.
type portOwner struct {
	proto   uint8
	port    uint16
	service bool // the owning process runs in session 0
}

// serviceOnly reduces sockets to the ports that no desktop program shares.
func serviceOnly(owners []portOwner) map[uint32]bool {
	m := make(map[uint32]bool, len(owners))
	for _, o := range owners {
		k := serviceKey(o.proto, o.port)
		if !o.service {
			m[k] = false
		} else if _, seen := m[k]; !seen {
			m[k] = true
		}
	}
	for k, v := range m {
		if !v {
			delete(m, k)
		}
	}
	return m
}
