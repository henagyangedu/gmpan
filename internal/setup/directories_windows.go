//go:build windows

package setup

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

func (w *Wizard) browseDirectories(out http.ResponseWriter, r *http.Request) {
	type directory struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	items := []directory{}
	path := r.URL.Query().Get("path")
	if path == "" {
		mask, err := windows.GetLogicalDrives()
		if err != nil {
			reply(out, 400, map[string]string{"message": "无法读取本机磁盘，可直接填写完整目录路径"})
			return
		}
		for i := 0; i < 26; i++ {
			if mask&(1<<i) == 0 {
				continue
			}
			root := string(rune('A'+i)) + `:\`
			ptr, _ := windows.UTF16PtrFromString(root)
			kind := windows.GetDriveType(ptr)
			if kind == windows.DRIVE_FIXED || kind == windows.DRIVE_REMOVABLE {
				items = append(items, directory{root, root})
			}
		}
		reply(out, 200, map[string]any{"path": "", "parent": "", "items": items, "selectable": false, "truncated": false, "missing": false})
		return
	}
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || len(filepath.VolumeName(path)) != 2 || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n\"%") {
		reply(out, 400, map[string]string{"message": "请填写本机磁盘上的完整目录路径"})
		return
	}
	path = filepath.Clean(path)
	missing := false
	for {
		_, err := os.Stat(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(path) == path {
			reply(out, 400, map[string]string{"message": "该目录不可访问，请检查磁盘和读取权限"})
			return
		}
		path = filepath.Dir(path)
		missing = true
	}
	// Check ancestors as well, so an elevated wizard never follows junctions.
	root := filepath.VolumeName(path) + `\`
	if !strings.EqualFold(path, root) {
		if err := localPath(path); err != nil {
			reply(out, 400, map[string]string{"message": err.Error()})
			return
		}
	}
	dir, err := os.Open(path)
	if err != nil {
		reply(out, 400, map[string]string{"message": "无法打开目录，请检查读取权限"})
		return
	}
	defer dir.Close()
	visited, truncated := 0, false
	for visited < 5000 && len(items) < 200 {
		entries, readErr := dir.ReadDir(100)
		visited += len(entries)
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.EqualFold(entry.Name(), ".gmpan") {
				continue
			}
			child := filepath.Join(path, entry.Name())
			actual, e := filepath.EvalSymlinks(child)
			if e != nil || !strings.EqualFold(filepath.Clean(actual), child) {
				continue
			}
			items = append(items, directory{entry.Name(), child})
			if len(items) >= 200 {
				truncated = true
				break
			}
		}
		if readErr != nil && readErr != io.EOF {
			reply(out, 400, map[string]string{"message": "无法读取目录列表，请检查读取权限"})
			return
		}
		if readErr != nil {
			break
		}
		truncated = visited >= 5000 || truncated
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	reply(out, 200, map[string]any{"path": path, "parent": parent, "items": items, "selectable": localPath(path) == nil, "truncated": truncated, "missing": missing})
}
