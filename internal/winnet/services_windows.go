//go:build windows

package winnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// serviceTableMaxAge is how old the port owner table may get while traffic
// arrives. A service that opens a new port is unprotected for at most this
// long.
const serviceTableMaxAge = time.Second

// NewServiceFilter returns the filter for this machine.
func NewServiceFilter() *ServiceFilter {
	return newServiceFilter(loadServicePorts, serviceTableMaxAge)
}

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet                  = 2
	tcpTableOwnerPIDListen  = 3 // TCP_TABLE_OWNER_PID_LISTENER
	udpTableOwnerPID        = 1 // UDP_TABLE_OWNER_PID
	tcpRowOwnerPIDSize      = 24
	udpRowOwnerPIDSize      = 12
	errorInsufficientBuffer = 122
)

// extendedTable calls GetExtendedTcpTable or GetExtendedUdpTable and returns
// the rows after the entry count.
func extendedTable(proc *windows.LazyProc, class uint32) (rows []byte, n int, err error) {
	size := uint32(16 * 1024)
	for range 4 {
		buf := make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, uintptr(class), 0)
		switch r {
		case 0:
			count := int(binary.LittleEndian.Uint32(buf[:4]))
			return buf[4:], count, nil
		case errorInsufficientBuffer:
			size += 4096 // the table can grow between the two calls
		default:
			return nil, 0, windows.Errno(r)
		}
	}
	return nil, 0, errors.New("winnet: socket table keeps growing")
}

// reachable reports whether a socket bound to addr receives packets sent to
// the virtual network: bound to all addresses or to a virtual address.
func reachable(addr [4]byte) bool {
	a := netip.AddrFrom4(addr)
	return a.IsUnspecified() || VirtualNetwork.Contains(a)
}

// loadServicePorts lists the TCP listeners and UDP sockets reachable from the
// virtual network and returns the ports that only session-0 processes own.
func loadServicePorts() (map[uint32]bool, error) {
	sessions := map[uint32]bool{} // pid -> runs in session 0
	isService := func(pid uint32) bool {
		if s, ok := sessions[pid]; ok {
			return s
		}
		var sid uint32
		// The Idle and System processes, and any process we cannot ask
		// (protected system processes), count as services.
		s := pid == 0 || pid == 4 || windows.ProcessIdToSessionId(pid, &sid) != nil || sid == 0
		sessions[pid] = s
		return s
	}
	var owners []portOwner
	rows, n, err := extendedTable(procGetExtendedTcpTable, tcpTableOwnerPIDListen)
	if err != nil {
		return nil, err
	}
	for i := 0; i < n && (i+1)*tcpRowOwnerPIDSize <= len(rows); i++ {
		r := rows[i*tcpRowOwnerPIDSize:]
		// dwState, dwLocalAddr, dwLocalPort, dwRemoteAddr, dwRemotePort, dwOwningPid
		if !reachable([4]byte(r[4:8])) {
			continue
		}
		port := binary.BigEndian.Uint16(r[8:10])
		pid := binary.LittleEndian.Uint32(r[20:24])
		owners = append(owners, portOwner{proto: ProtoTCP, port: port, service: isService(pid)})
	}
	rows, n, err = extendedTable(procGetExtendedUdpTable, udpTableOwnerPID)
	if err != nil {
		return nil, err
	}
	for i := 0; i < n && (i+1)*udpRowOwnerPIDSize <= len(rows); i++ {
		r := rows[i*udpRowOwnerPIDSize:]
		// dwLocalAddr, dwLocalPort, dwOwningPid
		if !reachable([4]byte(r[0:4])) {
			continue
		}
		port := binary.BigEndian.Uint16(r[4:6])
		pid := binary.LittleEndian.Uint32(r[8:12])
		owners = append(owners, portOwner{proto: ProtoUDP, port: port, service: isService(pid)})
	}
	return serviceOnly(owners), nil
}
