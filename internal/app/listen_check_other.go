//go:build !windows

package app

import "net"

func checkIPv4Listeners(net.IP, uint16) error { return nil }
