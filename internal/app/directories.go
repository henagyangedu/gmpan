package app

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Server) hiddenDirectory(path string) bool {
	if strings.EqualFold(filepath.Base(path), ".gmpan") {
		return true
	}
	for _, protected := range []string{deploymentDirectory(), s.Config.LogDir} {
		if protected == "" || protected == "." {
			continue
		}
		if within(realIfExisting(protected), path) {
			return true
		}
	}
	return false
}

func (s *Server) browseDirectories(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := superAdmin(u); err != nil {
		return err
	}
	path := r.URL.Query().Get("path")
	type directory struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	items := []directory{}
	if path == "" {
		for _, root := range directoryRoots() {
			items = append(items, directory{root, root})
		}
		writeJSON(w, 200, map[string]any{"path": "", "parent": "", "items": items, "selectable": false, "truncated": false})
		return nil
	}
	if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
		return bad("请输入服务器上的完整目录路径")
	}
	path = filepath.Clean(path)
	real, err := canonicalRoot(path)
	if err != nil || !sameDirectory(real, path) || s.hiddenDirectory(real) {
		return bad("该服务器目录不可访问，或属于系统保护目录、链接目录")
	}
	dir, err := os.Open(real)
	if err != nil {
		return bad("服务账号无法读取该服务器目录")
	}
	defer dir.Close()
	truncated := false
	// Bound directory enumeration, including directories with very many files.
	visited := 0
	for visited < 10000 && len(items) < 200 {
		entries, readErr := dir.ReadDir(100)
		visited += len(entries)
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := filepath.Join(real, entry.Name())
			actual, e := filepath.EvalSymlinks(child)
			if e != nil || !sameDirectory(actual, child) || s.hiddenDirectory(child) {
				continue
			}
			items = append(items, directory{entry.Name(), child})
			if len(items) >= 200 {
				truncated = true
				break
			}
		}
		if readErr != nil && readErr != io.EOF {
			return bad("服务账号无法读取该服务器目录内容")
		}
		if readErr != nil {
			break
		}
		if visited >= 10000 {
			truncated = true
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	selectable := validateStorageRoot(real, s.Config) == nil
	parent := filepath.Dir(real)
	if parent == real {
		parent = ""
	}
	writeJSON(w, 200, map[string]any{"path": real, "parent": parent, "items": items, "selectable": selectable, "truncated": truncated})
	return nil
}
