//go:build windows

package app

import (
	"encoding/binary"
	"errors"
	"net"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetTcpTable")

func checkIPv4Listeners(ip net.IP, port uint16) error {
	if ip.To4() == nil {
		return nil
	}
	// MIB_TCPTABLE has a DWORD count followed by 20-byte MIB_TCPROW records.
	// Ports are network byte order; only their low 16 bits are defined.
	// https://learn.microsoft.com/windows/win32/api/iphlpapi/nf-iphlpapi-gettcptable
	size := uint32(16 << 10)
	for attempt := 0; attempt < 3; attempt++ {
		if size < 4 || size > 8<<20 {
			break
		}
		buf := make([]byte, size)
		code, _, _ := getTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
		runtime.KeepAlive(buf)
		if code == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if code != 0 {
			break
		}
		count := int(binary.LittleEndian.Uint32(buf[:4]))
		if count > (len(buf)-4)/20 {
			break
		}
		for i := 0; i < count; i++ {
			row := buf[4+i*20 : 4+(i+1)*20]
			if binary.LittleEndian.Uint32(row[:4]) != 2 || binary.BigEndian.Uint16(row[8:10]) != port {
				continue
			}
			local := net.IPv4(row[4], row[5], row[6], row[7])
			if local.IsUnspecified() || ip.IsUnspecified() || local.Equal(ip) {
				return errors.New("该端口已有程序监听，请更换端口")
			}
		}
		return nil
	}
	return errors.New("无法读取 Windows 端口占用信息，请稍后重试")
}
