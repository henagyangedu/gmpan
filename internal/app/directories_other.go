//go:build !windows

package app

func directoryRoots() []string { return []string{"/"} }
