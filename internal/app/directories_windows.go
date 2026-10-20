//go:build windows

package app

import "os"

func directoryRoots() []string {
	roots := []string{}
	for letter := 'A'; letter <= 'Z'; letter++ {
		path := string(letter) + `:\`
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}
