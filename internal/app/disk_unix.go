//go:build !windows

package app

import "golang.org/x/sys/unix"

func diskSpace(path string) (uint64, uint64, error) {
	var v unix.Statfs_t
	e := unix.Statfs(path, &v)
	return uint64(v.Bavail) * uint64(v.Bsize), uint64(v.Blocks) * uint64(v.Bsize), e
}
