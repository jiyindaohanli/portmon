package win32

import (
	"net"
	"sort"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modIphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = modIphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet  = 2
	afInet6 = 23

	tcpTableOwnerPIDAll = 5
	udpTableOwnerPID    = 1
)

// MIB 结构体布局与 Windows SDK winsock2.h / iphlpapi.h 一致，改动前务必核对。
type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

type mibTCP6RowOwnerPID struct {
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
	State         uint32
	OwningPID     uint32
}

type mibUDPRowOwnerPID struct {
	LocalAddr uint32
	LocalPort uint32
	OwningPID uint32
}

type mibUDP6RowOwnerPID struct {
	LocalAddr    [16]byte
	LocalScopeID uint32
	LocalPort    uint32
	OwningPID    uint32
}

// Row 是一条套接字记录，TCP 与 UDP 共用。
type Row struct {
	Proto      string
	Addr       string
	Port       int
	RemoteAddr string
	RemotePort int
	State      string
	PID        uint32
}

var tcpStates = map[uint32]string{
	1: "CLOSED", 2: "LISTEN", 3: "SYN_SENT", 4: "SYN_RCVD", 5: "ESTABLISHED",
	6: "FIN_WAIT1", 7: "FIN_WAIT2", 8: "CLOSE_WAIT", 9: "CLOSING", 10: "LAST_ACK",
	11: "TIME_WAIT", 12: "DELETE_TCB",
}

// queryTable 两次调用法：先取所需字节数，再取数据。表在两次调用间可能增长，故重试。
func queryTable(proc *windows.LazyProc, family, class uint32) ([]byte, error) {
	var size uint32
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(family), uintptr(class), 0)
	errno := syscall.Errno(r)
	if errno != windows.ERROR_INSUFFICIENT_BUFFER && errno != windows.ERROR_SUCCESS {
		return nil, errno
	}

	for attempt := 0; attempt < 4; attempt++ {
		if size == 0 {
			return nil, nil
		}
		buf := make([]byte, size)
		n := size
		r, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0,
			uintptr(family), uintptr(class), 0)
		errno = syscall.Errno(r)
		if errno == windows.ERROR_SUCCESS {
			return buf[:n], nil
		}
		if errno != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, errno
		}
		size = n
	}
	return nil, windows.ERROR_INSUFFICIENT_BUFFER
}

// ntohs 端口字段是网络字节序（大端）存在 DWORD 低 16 位里。
func ntohs(p uint32) int {
	return int(uint16(p>>8&0xff) | uint16(p&0xff)<<8)
}

// v4 地址同样是大端存放。
func v4(a uint32) string {
	return net.IPv4(byte(a), byte(a>>8), byte(a>>16), byte(a>>24)).String()
}

func appendRows(rows []Row, buf []byte, size uintptr, decode func(unsafe.Pointer) Row) []Row {
	if len(buf) < 4 {
		return rows
	}
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	for i := uint32(0); i < count; i++ {
		off := 4 + uintptr(i)*size
		if off+size > uintptr(len(buf)) {
			break
		}
		rows = append(rows, decode(unsafe.Pointer(&buf[off])))
	}
	return rows
}

// TCPTable 返回 IPv4 + IPv6 全部 TCP 记录（含监听与已建立）。
func TCPTable() ([]Row, error) {
	var rows []Row
	var firstErr error

	if buf, err := queryTable(procGetExtendedTcpTable, afInet, tcpTableOwnerPIDAll); err != nil {
		firstErr = err
	} else {
		rows = appendRows(rows, buf, unsafe.Sizeof(mibTCPRowOwnerPID{}), func(p unsafe.Pointer) Row {
			r := (*mibTCPRowOwnerPID)(p)
			return Row{
				Proto: "TCP", Addr: v4(r.LocalAddr), Port: ntohs(r.LocalPort),
				RemoteAddr: v4(r.RemoteAddr), RemotePort: ntohs(r.RemotePort),
				State: tcpStates[r.State], PID: r.OwningPID,
			}
		})
	}

	if buf, err := queryTable(procGetExtendedTcpTable, afInet6, tcpTableOwnerPIDAll); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		rows = appendRows(rows, buf, unsafe.Sizeof(mibTCP6RowOwnerPID{}), func(p unsafe.Pointer) Row {
			r := (*mibTCP6RowOwnerPID)(p)
			return Row{
				Proto: "TCP6", Addr: net.IP(r.LocalAddr[:]).String(), Port: ntohs(r.LocalPort),
				RemoteAddr: net.IP(r.RemoteAddr[:]).String(), RemotePort: ntohs(r.RemotePort),
				State: tcpStates[r.State], PID: r.OwningPID,
			}
		})
	}

	if len(rows) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return rows, nil
}

// UDPTable 返回 UDP 绑定记录，UDP 无连接状态，统一标记 BOUND。
func UDPTable() ([]Row, error) {
	var rows []Row
	var firstErr error

	if buf, err := queryTable(procGetExtendedUdpTable, afInet, udpTableOwnerPID); err != nil {
		firstErr = err
	} else {
		rows = appendRows(rows, buf, unsafe.Sizeof(mibUDPRowOwnerPID{}), func(p unsafe.Pointer) Row {
			r := (*mibUDPRowOwnerPID)(p)
			return Row{Proto: "UDP", Addr: v4(r.LocalAddr), Port: ntohs(r.LocalPort), State: "BOUND", PID: r.OwningPID}
		})
	}

	if buf, err := queryTable(procGetExtendedUdpTable, afInet6, udpTableOwnerPID); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		rows = appendRows(rows, buf, unsafe.Sizeof(mibUDP6RowOwnerPID{}), func(p unsafe.Pointer) Row {
			r := (*mibUDP6RowOwnerPID)(p)
			return Row{Proto: "UDP6", Addr: net.IP(r.LocalAddr[:]).String(), Port: ntohs(r.LocalPort), State: "BOUND", PID: r.OwningPID}
		})
	}

	if len(rows) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return rows, nil
}

// SortRows 按端口升序，端口相同按 PID，便于前端差量渲染时保持稳定顺序。
func SortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Port != rows[j].Port {
			return rows[i].Port < rows[j].Port
		}
		if rows[i].PID != rows[j].PID {
			return rows[i].PID < rows[j].PID
		}
		return rows[i].Proto < rows[j].Proto
	})
}
