package app

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const folderMaxFiles = 500
const folderMaxEntries = 1000
const folderMaxDepth = 32
const folderMaxBytes int64 = 2 << 30
const archiveReserve uint64 = 16 << 20

func folderTransferLimits() map[string]any {
	return map[string]any{"max_files": folderMaxFiles, "max_entries": folderMaxEntries, "max_depth": folderMaxDepth, "max_bytes": folderMaxBytes}
}

type archiveItem struct {
	entry *Entry
	name  string
	depth int
}
type folderArchive struct {
	token, userID, path, name, action string
	items                             []archiveItem
	expires                           time.Time
	timer                             *time.Timer
}

// Only authorized indexed entries are walked. Never ZIP the physical storage root.
func (s *Server) archiveItems(ctx context.Context, u *User, root *Entry) ([]archiveItem, int64, error) {
	if root.Kind != "folder" || root.Deleted {
		return nil, 0, notfound()
	}
	if !validName(root.Name) || strings.Contains(root.Name, ":") {
		return nil, 0, bad("目录名称不适合打包，请先重命名")
	}
	items := []archiveItem{{root, root.Name + "/", 1}}
	var total int64
	files := 0
	for index := 0; index < len(items); index++ {
		item := items[index]
		if item.entry.Kind != "folder" {
			continue
		}
		query := "SELECT " + entryFields + " FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.parent_id=? AND e.space_key=? AND e.deleted=0"
		args := []any{item.entry.ID, root.SpaceKey}
		if root.Scope != "personal" && !operations(u) && !(root.Scope == "department" && u.Role == "dept_admin") {
			query += " AND (e.publication='published' OR e.owner_id=?)"
			args = append(args, u.ID)
		}
		query += " ORDER BY e.name,e.id LIMIT ?"
		args = append(args, folderMaxEntries+1)
		rows, e := s.DB.QueryContext(ctx, query, args...)
		if e != nil {
			return nil, 0, e
		}
		for rows.Next() {
			v, err := scanEntry(rows)
			if err != nil {
				rows.Close()
				return nil, 0, err
			}
			if !canRead(u, v) {
				continue
			}
			depth := item.depth
			if v.Kind == "folder" {
				depth++
			}
			if len(items) >= folderMaxEntries || depth > folderMaxDepth || !validName(v.Name) || strings.Contains(v.Name, ":") || len(item.name)+len(v.Name) > 2048 {
				rows.Close()
				return nil, 0, bad("目录层级、项目数量或路径超出打包限制，请分批下载")
			}
			name := item.name + v.Name
			if v.Kind == "folder" {
				name += "/"
			} else if v.Kind == "file" {
				files++
				if v.Size < 0 || v.Size > folderMaxBytes-total || files > folderMaxFiles {
					rows.Close()
					return nil, 0, bad("目录下载每次最多 500 个文件、2 GiB，请分批下载")
				}
				total += v.Size
				if !s.scanAccess(v) {
					rows.Close()
					return nil, 0, apiError{423, "目录中有未通过安全检查或缺失的可见文件，请处理后再下载"}
				}
			} else {
				rows.Close()
				return nil, 0, bad("目录中存在无效项目")
			}
			items = append(items, archiveItem{v, name, depth})
		}
		err := rows.Err()
		rows.Close()
		if err != nil {
			return nil, 0, err
		}
	}
	return items, total, nil
}

type archiveReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r archiveReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.reader.Read(p)
}
func (s *Server) writeArchive(ctx context.Context, output io.Writer, items []archiveItem) error {
	w := zip.NewWriter(output)
	buffer := make([]byte, 128<<10)
	for _, item := range items {
		if e := ctx.Err(); e != nil {
			return e
		}
		v := item.entry
		header := &zip.FileHeader{Name: item.name, Method: zip.Store, Modified: v.UpdatedAt}
		if v.Kind == "folder" {
			header.SetMode(os.ModeDir | 0755)
			if _, e := w.CreateHeader(header); e != nil {
				return e
			}
			continue
		}
		path, e := s.entryPath(v)
		if e != nil {
			return apiError{410, "目录中有不可读取的源文件，请重新扫描"}
		}
		f, e := os.Open(path)
		if e != nil {
			return apiError{410, "目录中有不可读取的源文件"}
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		if !info.Mode().IsRegular() || info.Size() != v.Size || info.ModTime().UnixNano() != v.SourceMtime {
			f.Close()
			s.DB.Exec("UPDATE entries SET scan_status='pending',scan_note='源文件已变更，等待重新检查' WHERE id=?", v.ID)
			return apiError{423, "目录中的源文件已变更，请重新检查后下载"}
		}
		header.Modified = info.ModTime()
		header.SetMode(0644)
		dest, e := w.CreateHeader(header)
		if e != nil {
			f.Close()
			return e
		}
		hash := sha256.New()
		n, copyErr := io.CopyBuffer(io.MultiWriter(dest, hash), archiveReader{ctx, io.LimitReader(f, v.Size+1)}, buffer)
		after, statErr := f.Stat()
		f.Close()
		if copyErr != nil {
			return copyErr
		}
		if statErr != nil || n != v.Size || after.Size() != v.Size || !after.ModTime().Equal(info.ModTime()) || (v.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != v.SHA256) {
			s.DB.Exec("UPDATE entries SET scan_status='pending',scan_note='源文件已变更，等待重新检查' WHERE id=?", v.ID)
			return apiError{423, "目录文件在打包期间发生变化，请重新检查后下载"}
		}
	}
	return w.Close()
}
func (s *Server) releaseArchive() { s.archiveMu.Lock(); s.archiveBusy = false; s.archiveMu.Unlock() }
func (s *Server) prepareFolderDownload(w http.ResponseWriter, r *http.Request, u *User) error {
	root, e := s.getEntry(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	return s.prepareArchive(w, r, u, "folder_archive", func(ctx context.Context) ([]archiveItem, int64, string, error) {
		items, total, e := s.archiveItems(ctx, u, root)
		return items, total, root.Name + ".zip", e
	})
}
func (s *Server) prepareArchive(w http.ResponseWriter, r *http.Request, u *User, action string, collect func(context.Context) ([]archiveItem, int64, string, error)) error {
	s.archiveMu.Lock()
	if s.archiveBusy {
		s.archiveMu.Unlock()
		return apiError{429, "已有目录正在打包或下载，请稍后重试"}
	}
	s.archiveBusy = true
	s.archiveMu.Unlock()
	ready := false
	defer func() {
		if !ready {
			s.releaseArchive()
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	items, total, name, e := collect(ctx)
	if e != nil {
		return e
	}
	threshold := 10
	s.siteMu.RLock()
	if s.site.StorageWarningPercent > 0 {
		threshold = s.site.StorageWarningPercent
	}
	s.siteMu.RUnlock()
	available, capacity, e := diskSpace(s.Config.DataDir)
	needed := uint64(total) + archiveReserve
	if e != nil || capacity == 0 || available <= needed || storageLow(available-needed, capacity, threshold) {
		return apiError{507, "可用空间不足以安全生成 ZIP，请先维护磁盘或分批下载"}
	}
	dir := filepath.Join(s.Config.DataDir, "downloads")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if !sameDirectory(realIfExisting(dir), dir) {
		return bad("下载临时目录不能是链接或目录联接")
	}
	f, e := os.CreateTemp(dir, "pack-*.zip")
	if e != nil {
		return e
	}
	defer func() {
		f.Close()
		if !ready {
			os.Remove(f.Name())
		}
	}()
	if e = s.writeArchive(ctx, f, items); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	job := &folderArchive{token: newID(), userID: u.ID, path: f.Name(), name: name, action: action, items: items, expires: time.Now().Add(5 * time.Minute)}
	s.archiveMu.Lock()
	s.archiveJob = job
	job.timer = time.AfterFunc(5*time.Minute, func() {
		s.archiveMu.Lock()
		if s.archiveJob != job {
			s.archiveMu.Unlock()
			return
		}
		s.archiveJob = nil
		s.archiveBusy = false
		s.archiveMu.Unlock()
		os.Remove(job.path)
	})
	s.archiveMu.Unlock()
	ready = true
	s.audit(u.ID, action+"_prepare", items[0].entry.ID, "ok")
	writeJSON(w, 201, map[string]any{"url": "/api/folder-downloads/" + job.token, "name": job.name, "files": countArchiveFiles(items), "bytes": total, "expires_at": job.expires})
	return nil
}
func countArchiveFiles(items []archiveItem) int {
	n := 0
	for _, item := range items {
		if item.entry.Kind == "file" {
			n++
		}
	}
	return n
}
func (s *Server) downloadFolderArchive(w http.ResponseWriter, r *http.Request, u *User) error {
	s.archiveMu.Lock()
	job := s.archiveJob
	if job == nil || job.token != r.PathValue("token") || job.userID != u.ID || time.Now().After(job.expires) {
		s.archiveMu.Unlock()
		return notfound()
	}
	s.archiveJob = nil
	job.timer.Stop()
	s.archiveMu.Unlock()
	defer s.releaseArchive()
	defer os.Remove(job.path)
	// Check permissions again in case an account, department, publication or trash state changed.
	for _, item := range job.items {
		current, e := s.getEntry(item.entry.ID, u)
		if e != nil {
			return notfound()
		}
		if current.Deleted || current.SpaceKey != item.entry.SpaceKey || (current.Kind == "file" && (!s.scanAccess(current) || current.StoragePath != item.entry.StoragePath || current.SourceMtime != item.entry.SourceMtime || current.Size != item.entry.Size)) {
			return conflict("目录内容或权限已变化，请重新打包")
		}
	}
	f, e := os.Open(job.path)
	if e != nil {
		return notfound()
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return fmt.Errorf("读取临时下载失败: %w", e)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": job.name}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	s.audit(u.ID, job.action+"_download", job.items[0].entry.ID, "ok")
	http.ServeContent(w, r, job.name, info.ModTime(), f)
	return nil
}

// Remove only stale ZIPs left after an interrupted process, never indexed user files.
func (s *Server) cleanupFolderDownloads() {
	dir := filepath.Join(s.Config.DataDir, "downloads")
	if !sameDirectory(realIfExisting(dir), dir) {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "pack-") || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		info, e := entry.Info()
		if e == nil && info.Mode().IsRegular() {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
