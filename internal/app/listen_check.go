package app

import (
	"net"
	"strconv"
)

// CheckListenAddress checks existing listeners before attempting a temporary
// bind, so IPv4 wildcard listeners on Windows cannot be shadowed by a specific IP.
func CheckListenAddress(address string) error {
	host, text, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return bad("监听端口需为 1～65535")
	}
	if host == "" {
		host = "0.0.0.0"
	}
	if err := checkIPv4Listeners(net.ParseIP(host), uint16(port)); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err == nil {
		listener.Close()
	}
	return err
}
