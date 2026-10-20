package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Upload struct {
	ID          string            `json:"id"`
	UserID      string            `json:"-"`
	SpaceKey    string            `json:"-"`
	Scope       string            `json:"scope"`
	DeptID      string            `json:"-"`
	ParentID    string            `json:"parent_id"`
	Name        string            `json:"name"`
	Remark      string            `json:"remark"`
	Size        int64             `json:"size"`
	TotalChunks int               `json:"total_chunks"`
	Status      string            `json:"status"`
	EntryID     string            `json:"entry_id"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Chunks      []int             `json:"chunks"`
	ChunkHashes map[string]string `json:"chunk_hashes"`
}

const uploadFields = "id,user_id,space_key,scope,dept_id,parent_id,name,remark,size,total_chunks,status,entry_id,expires_at"

func readUpload(row rowScanner) (*Upload, error) {
	v := new(Upload)
	e := row.Scan(&v.ID, &v.UserID, &v.SpaceKey, &v.Scope, &v.DeptID, &v.ParentID, &v.Name, &v.Remark, &v.Size, &v.TotalChunks, &v.Status, &v.EntryID, &v.ExpiresAt)
	v.Chunks = []int{}
	return v, e
}
func (s *Server) getUpload(id string, u *User) (*Upload, error) {
	v, e := readUpload(s.DB.QueryRow("SELECT "+uploadFields+" FROM uploads WHERE id=? AND user_id=?", id, u.ID))
	if e == sql.ErrNoRows {
		return nil, notfound()
	}
	if e != nil {
		return nil, e
	}
	if !uploadSpaceAccessible(u, v) {
		return nil, notfound()
	}
	return v, nil
}
func (s *Server) listUploads(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.DB.Query("SELECT "+uploadFields+" FROM uploads WHERE user_id=? AND status='uploading' AND expires_at>NOW() ORDER BY created_at DESC", u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []*Upload{}
	for rows.Next() {
		v, e := readUpload(rows)
		if e != nil {
			return e
		}
		if uploadSpaceAccessible(u, v) {
			out = append(out, v)
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request, u *User) error {
	v, e := s.getUpload(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	v.ChunkHashes = map[string]string{}
	rows, e := s.DB.Query("SELECT chunk_index,sha256,size FROM upload_chunks WHERE upload_id=? ORDER BY chunk_index", v.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var hash string
		var partSize int64
		if e = rows.Scan(&n, &hash, &partSize); e != nil {
			return e
		}
		info, err := os.Stat(filepath.Join(s.Config.DataDir, "chunks", v.ID, fmt.Sprintf("%06d.part", n)))
		if err != nil || info.Size() != partSize {
			continue
		}
		v.Chunks = append(v.Chunks, n)
		v.ChunkHashes[strconv.Itoa(n)] = hash
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, v)
	return nil
}
func allowedExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp", ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".csv", ".json", ".mp4", ".webm", ".mov", ".avi", ".mkv", ".mp3", ".wav", ".ogg", ".m4a", ".zip", ".rar", ".7z", ".tar", ".gz", ".exe", ".msi":
		return true
	}
	return false
}
func validateHeader(name string, b []byte) bool {
	ext := strings.ToLower(filepath.Ext(name))
	detected := http.DetectContentType(b)
	switch ext {
	case ".pdf":
		return bytes.HasPrefix(b, []byte("%PDF-"))
	case ".jpg", ".jpeg":
		return detected == "image/jpeg"
	case ".png":
		return detected == "image/png"
	case ".gif":
		return detected == "image/gif"
	case ".webp":
		return detected == "image/webp"
	case ".bmp":
		return detected == "image/bmp"
	case ".exe":
		return bytes.HasPrefix(b, []byte("MZ"))
	case ".zip", ".docx", ".xlsx", ".pptx":
		return bytes.HasPrefix(b, []byte("PK\x03\x04")) || bytes.HasPrefix(b, []byte("PK\x05\x06"))
	case ".doc", ".xls", ".ppt", ".msi":
		return bytes.HasPrefix(b, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	case ".rar":
		return bytes.HasPrefix(b, []byte("Rar!\x1a\x07"))
	case ".7z":
		return bytes.HasPrefix(b, []byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c})
	case ".gz":
		return bytes.HasPrefix(b, []byte{0x1f, 0x8b})
	case ".mp4", ".mov", ".m4a", ".avif":
		return len(b) >= 12 && string(b[4:8]) == "ftyp"
	case ".webm", ".mkv":
		return bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3})
	case ".avi":
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "AVI "
	case ".wav":
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE"
	case ".ogg":
		return bytes.HasPrefix(b, []byte("OggS"))
	case ".mp3":
		return bytes.HasPrefix(b, []byte("ID3")) || len(b) > 1 && b[0] == 0xff && b[1]&0xe0 == 0xe0
	case ".txt", ".md", ".csv", ".json":
		return len(b) == 0 || strings.HasPrefix(detected, "text/plain") || detected == "application/json"
	case ".tar":
		return len(b) >= 262 && string(b[257:262]) == "ustar"
	}
	return false
}
func (s *Server) initUpload(w http.ResponseWriter, r *http.Request, u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.directoryChangePending(true) {
		return conflict("存储目录等待生效，请管理员重启服务后再上传")
	}
	var p struct {
		Scope    string `json:"scope"`
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
		Remark   string `json:"remark"`
		Size     int64  `json:"size"`
		OwnerID  string `json:"owner_id"`
		DeptID   string `json:"dept_id"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if !validName(p.Name) || !allowedExtension(p.Name) {
		return bad("文件名或文件类型不支持")
	}
	if e := validateRemark(p.Remark); e != nil {
		return e
	}
	if p.Size < 0 || p.Size > s.Config.MaxFileBytes {
		return bad("文件大小超过上传上限")
	}
	key, dept, owner, e := s.resolveSpace(u, p.Scope, p.OwnerID, p.DeptID)
	if e != nil {
		return e
	}
	if p.Scope == "department" && dept == "" {
		return bad("请先选择部门")
	}
	if e = s.checkParent(p.ParentID, key, u); e != nil {
		return e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var quota int64
	if e = tx.QueryRow("SELECT quota_bytes FROM users WHERE id=? FOR UPDATE", owner).Scan(&quota); e != nil {
		return e
	}
	var active int
	if e = tx.QueryRow("SELECT COUNT(*) FROM uploads WHERE user_id=? AND status='uploading' AND expires_at>NOW()", u.ID).Scan(&active); e != nil {
		return e
	}
	if active >= 10 {
		return bad("进行中的上传过多，请先完成或取消已有任务")
	}
	if p.Scope == "personal" {
		var used, reserved int64
		if e = tx.QueryRow("SELECT COALESCE(SUM(size),0) FROM entries WHERE space_key=? AND kind='file'", key).Scan(&used); e != nil {
			return e
		}
		if e = tx.QueryRow("SELECT COALESCE(SUM(size),0) FROM uploads WHERE space_key=? AND status='uploading' AND expires_at>NOW()", key).Scan(&reserved); e != nil {
			return e
		}
		if p.Size > quota-used-reserved {
			return bad("个人空间配额不足；回收站文件仍占用配额")
		}
	}
	var names int
	if e = tx.QueryRow("SELECT COUNT(*) FROM entries WHERE space_key=? AND parent_id=? AND name_hash=?", key, p.ParentID, digest(strings.ToLower(p.Name))).Scan(&names); e != nil {
		return e
	}
	if names > 0 {
		return conflict("当前目录存在同名项目，包括回收站项目，请更名或恢复")
	}
	id := newID()
	total := int((p.Size + ChunkSize - 1) / ChunkSize)
	if total == 0 {
		total = 1
	}
	expires := time.Now().Add(48 * time.Hour)
	if _, e = tx.Exec("INSERT INTO uploads(id,user_id,space_key,scope,dept_id,parent_id,name,remark,size,total_chunks,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", id, u.ID, key, p.Scope, dept, p.ParentID, p.Name, p.Remark, p.Size, total, expires); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(s.Config.DataDir, "chunks", id), 0700); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.audit(u.ID, "upload_init", id, "ok")
	writeJSON(w, 201, map[string]any{"id": id, "total_chunks": total, "chunk_size": ChunkSize, "expires_at": expires})
	return nil
}
func lockUpload(tx *sql.Tx, id, user string) (*Upload, error) {
	v, e := readUpload(tx.QueryRow("SELECT "+uploadFields+" FROM uploads WHERE id=? AND user_id=? FOR UPDATE", id, user))
	if e == sql.ErrNoRows {
		return nil, notfound()
	}
	if e != nil {
		return nil, e
	}
	return v, nil
}
func activeUpload(v *Upload, u *User) error {
	if !uploadSpaceAccessible(u, v) {
		return notfound()
	}
	if v.Status != "uploading" || time.Now().After(v.ExpiresAt) {
		return conflict("上传任务已结束或过期")
	}
	return nil
}
func (s *Server) putChunk(w http.ResponseWriter, r *http.Request, u *User) error {
	n, e := strconv.Atoi(r.PathValue("index"))
	if e != nil || n < 0 {
		return bad("分片编号不正确")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := lockUpload(tx, r.PathValue("id"), u.ID)
	if e != nil {
		return e
	}
	if e = activeUpload(v, u); e != nil {
		return e
	}
	if n >= v.TotalChunks {
		return bad("分片编号超出范围")
	}
	expected := ChunkSize
	if n == v.TotalChunks-1 {
		expected = v.Size - int64(n)*ChunkSize
	}
	r.Body = http.MaxBytesReader(w, r.Body, expected+1)
	dir := filepath.Join(s.Config.DataDir, "chunks", v.ID)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, "part-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, hash), r.Body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || written != expected {
		return bad("分片大小不正确，请重新上传该分片")
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	dest := filepath.Join(dir, fmt.Sprintf("%06d.part", n))
	if e = os.Remove(dest); e != nil && !os.IsNotExist(e) {
		return e
	}
	if e = os.Rename(tmp, dest); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO upload_chunks(upload_id,chunk_index,size,sha256) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE size=VALUES(size),sha256=VALUES(sha256)", v.ID, n, written, hex.EncodeToString(hash.Sum(nil))); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request, u *User) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := lockUpload(tx, r.PathValue("id"), u.ID)
	if e != nil {
		return e
	}
	if !uploadSpaceAccessible(u, v) {
		return notfound()
	}
	if v.Status == "completed" {
		writeJSON(w, 200, map[string]string{"id": v.EntryID})
		return nil
	}
	if e = activeUpload(v, u); e != nil {
		return e
	}
	if v.ParentID != "" {
		var deleted bool
		var key string
		if e = tx.QueryRow("SELECT deleted,space_key FROM entries WHERE id=? AND kind='folder' FOR UPDATE", v.ParentID).Scan(&deleted, &key); e != nil {
			return bad("目标目录不可用")
		}
		if deleted || key != v.SpaceKey {
			return bad("目标目录不可用")
		}
	}
	var count int
	var bytesUploaded int64
	if e = tx.QueryRow("SELECT COUNT(*),COALESCE(SUM(size),0) FROM upload_chunks WHERE upload_id=?", v.ID).Scan(&count, &bytesUploaded); e != nil {
		return e
	}
	if count != v.TotalChunks || bytesUploaded != v.Size {
		return conflict("分片尚未完整上传")
	}
	id := newID()
	key := filepath.ToSlash(filepath.Join(id[:2], id))
	if s.Config.StorageRoot != "" {
		key = managedObjectKey(v.Scope, uploadOwner(v), v.DeptID, key)
	}
	dest := filepath.Join(s.Config.DataDir, "objects", filepath.FromSlash(key))
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	committed := false
	defer func() {
		f.Close()
		if !committed {
			os.Remove(dest)
		}
	}()
	var written int64
	for n := 0; n < v.TotalChunks; n++ {
		part, e := os.Open(filepath.Join(s.Config.DataDir, "chunks", v.ID, fmt.Sprintf("%06d.part", n)))
		if e != nil {
			return conflict("分片文件缺失，请重新上传")
		}
		nBytes, copyErr := io.Copy(f, part)
		part.Close()
		written += nBytes
		if copyErr != nil {
			return copyErr
		}
	}
	if written != v.Size {
		return conflict("文件大小不一致，请重新上传")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	headerFile, e := os.Open(dest)
	if e != nil {
		return e
	}
	header := make([]byte, 512)
	n, _ := headerFile.Read(header)
	headerFile.Close()
	if !validateHeader(v.Name, header[:n]) {
		return bad("文件内容与扩展名不匹配，已拒绝入库")
	}
	info, e := os.Stat(dest)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO entries(id,parent_id,space_key,scope,owner_id,dept_id,kind,name,name_hash,remark,size,mime,storage_kind,storage_path,source_mtime,publication) VALUES(?,?,?,?,?,?,'file',?,?,?,?,?,'local',?,?,?)", id, v.ParentID, v.SpaceKey, v.Scope, uploadOwner(v), v.DeptID, v.Name, digest(strings.ToLower(v.Name)), v.Remark, v.Size, http.DetectContentType(header[:n]), key, info.ModTime().UnixNano(), publication(u, v.Scope))
	if duplicate(e) {
		return conflict("当前目录已有同名项目，请取消后更换名称上传")
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE uploads SET status='completed',entry_id=? WHERE id=?", id, v.ID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	committed = true
	s.audit(u.ID, "upload_complete", id, "ok")
	writeJSON(w, 201, map[string]string{"id": id})
	return nil
}
func (s *Server) cancelUpload(w http.ResponseWriter, r *http.Request, u *User) error {
	v, e := s.getUpload(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	result, e := s.DB.Exec("UPDATE uploads SET status='cancelled',expires_at=NOW() WHERE id=? AND status='uploading'", v.ID)
	if e != nil {
		return e
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return conflict("上传已结束")
	}
	s.audit(u.ID, "upload_cancel", v.ID, "ok")
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
