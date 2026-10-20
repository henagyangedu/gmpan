//go:build windows

package app

import "golang.org/x/sys/windows"

func diskSpace(path string) (uint64, uint64, error) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return 0, 0, e
	}
	var available, total, free uint64
	e = windows.GetDiskFreeSpaceEx(p, &available, &total, &free)
	return available, total, e
}
