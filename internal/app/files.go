package app

import (
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

type Entry struct {
	ID            string    `json:"id"`
	ParentID      string    `json:"parent_id"`
	SpaceKey      string    `json:"-"`
	Scope         string    `json:"scope"`
	OwnerID       string    `json:"owner_id"`
	DeptID        string    `json:"dept_id"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	Remark        string    `json:"remark"`
	Size          int64     `json:"size"`
	MIME          string    `json:"mime"`
	StorageKind   string    `json:"storage_kind"`
	StoragePath   string    `json:"-"`
	SHA256        string    `json:"-"`
	ScanStatus    string    `json:"scan_status"`
	ScanNote      string    `json:"scan_note"`
	Publication   string    `json:"publication"`
	Deleted       bool      `json:"deleted"`
	TrashBatch    string    `json:"-"`
	SourceMtime   int64     `json:"-"`
	SourceMissing bool      `json:"source_missing"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	OwnerName     string    `json:"owner_name"`
	CanManage     bool      `json:"can_manage"`
	CanApprove    bool      `json:"can_approve"`
}

const entryFields = "e.id,e.parent_id,e.space_key,e.scope,e.owner_id,e.dept_id,e.kind,e.name,e.remark,e.size,e.mime,e.storage_kind,COALESCE(e.storage_path,''),e.sha256,e.scan_status,e.scan_note,e.publication,e.deleted,e.source_mtime,e.source_missing,e.created_at,e.updated_at,COALESCE(u.display_name,'历史资料'),e.trash_batch"

func scanEntry(row rowScanner) (*Entry, error) {
	v := new(Entry)
	e := row.Scan(&v.ID, &v.ParentID, &v.SpaceKey, &v.Scope, &v.OwnerID, &v.DeptID, &v.Kind, &v.Name, &v.Remark, &v.Size, &v.MIME, &v.StorageKind, &v.StoragePath, &v.SHA256, &v.ScanStatus, &v.ScanNote, &v.Publication, &v.Deleted, &v.SourceMtime, &v.SourceMissing, &v.CreatedAt, &v.UpdatedAt, &v.OwnerName, &v.TrashBatch)
	return v, e
}
func canRead(u *User, v *Entry) bool {
	if operations(u) && (v.Scope == "personal" || v.Scope == "department" || v.Scope == "company") {
		return true
	}
	switch v.Scope {
	case "personal":
		return v.OwnerID == u.ID
	case "department":
		if u.DeptID == "" || u.DeptID != v.DeptID {
			return false
		}
	case "company":
	default:
		return false
	}
	return v.Publication == "published" || v.OwnerID == u.ID || canModerate(u, v)
}
func canModerate(u *User, v *Entry) bool {
	return (v.Scope == "company" || v.Scope == "department") && operations(u) || v.Scope == "department" && v.DeptID != "" && v.DeptID == u.DeptID && u.Role == "dept_admin"
}
func canManage(u *User, v *Entry) bool {
	return canRead(u, v) && (operations(u) || u.ID == v.OwnerID || canModerate(u, v))
}
func (s *Server) getEntry(id string, u *User) (*Entry, error) {
	v, e := scanEntry(s.DB.QueryRow("SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.id=?", id))
	if e == sql.ErrNoRows {
		return nil, notfound()
	}
	if e != nil {
		return nil, e
	}
	if !canRead(u, v) {
		return nil, notfound()
	}
	v.CanManage = canManage(u, v)
	v.CanApprove = canModerate(u, v)
	return v, nil
}
func space(u *User, scope string) (string, string, error) {
	switch scope {
	case "personal":
		return "personal:" + u.ID, "", nil
	case "department":
		if u.DeptID == "" {
			return "", "", bad("尚未分配部门")
		}
		return "department:" + u.DeptID, u.DeptID, nil
	case "company":
		return "company", "", nil
	}
	return "", "", bad("空间类型不正确")
}
func escapedLike(q string) string {
	q = strings.ReplaceAll(q, "=", "==")
	q = strings.ReplaceAll(q, "%", "=%")
	q = strings.ReplaceAll(q, "_", "=_")
	return "%" + q + "%"
}
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request, u *User) error {
	q := r.URL.Query()
	scope := q.Get("scope")
	if scope == "" {
		scope = "company"
	}
	key, _, _, e := s.resolveSpace(u, scope, q.Get("owner"), q.Get("dept"))
	if e != nil {
		return e
	}
	where := " WHERE e.space_key=? AND e.deleted=?"
	args := []any{key, q.Get("trash") == "true"}
	if scope != "personal" && !operations(u) && !(scope == "department" && u.Role == "dept_admin") {
		where += " AND (e.publication='published' OR e.owner_id=?)"
		args = append(args, u.ID)
	}
	term := strings.TrimSpace(q.Get("q"))
	directory := scope == "company" && q.Get("directory") == "true" && q.Get("review") != "true" && q.Get("trash") != "true"
	if utf8.RuneCountInString(term) > 200 {
		return bad("搜索词不能超过 200 个字符")
	}
	if term != "" {
		where += " AND (e.name LIKE ? ESCAPE '=' OR e.remark LIKE ? ESCAPE '=')"
		args = append(args, escapedLike(term), escapedLike(term))
	}
	if directory {
		if e := s.checkParent(q.Get("parent"), key, u); e != nil {
			return e
		}
		if q.Get("recursive") == "true" {
			ids, e := s.companyDescendants(q.Get("parent"), u)
			if e != nil {
				return e
			}
			where += " AND e.parent_id IN (" + placeholders(len(ids)) + ")"
			for _, id := range ids {
				args = append(args, id)
			}
		} else {
			where += " AND e.parent_id=?"
			args = append(args, q.Get("parent"))
		}
	} else if term == "" && q.Get("review") != "true" && q.Get("trash") != "true" {
		where += " AND e.parent_id=?"
		args = append(args, q.Get("parent"))
	}
	if q.Get("review") == "true" {
		where += " AND e.publication='pending'"
	}
	if typ := q.Get("type"); typ != "" {
		condition := ""
		switch typ {
		case "folder":
			where += " AND e.kind='folder'"
		case "image", "video", "audio":
			condition = "e.kind='file' AND e.mime LIKE ?"
			args = append(args, typ+"/%")
		case "archive":
			condition = "e.kind='file' AND e.name REGEXP '[.](zip|rar|7z|tar|gz|tgz|bz2|xz)$'"
		case "resource":
			condition = "e.kind='file' AND e.mime NOT LIKE 'video/%' AND e.name NOT REGEXP '[.](zip|rar|7z|tar|gz|tgz|bz2|xz)$'"
		case "document":
			where += " AND e.kind='file' AND (e.mime='application/pdf' OR e.name REGEXP '\\.(docx?|xlsx?|pptx?|txt|md|csv)$')"
		case "other":
			where += " AND e.kind='file' AND e.mime NOT LIKE 'image/%' AND e.mime NOT LIKE 'video/%' AND e.mime NOT LIKE 'audio/%'"
		default:
			return bad("文件类型筛选不正确")
		}
		if condition != "" {
			if directory {
				where += " AND (e.kind='folder' OR (" + condition + "))"
			} else {
				where += " AND (" + condition + ")"
			}
		}
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		return bad("页码超出范围")
	}
	var total int
	if e = s.DB.QueryRow("SELECT COUNT(*) FROM entries e"+where, args...).Scan(&total); e != nil {
		return e
	}
	order := "e.updated_at DESC"
	switch q.Get("sort") {
	case "name":
		order = "e.name ASC"
	case "size":
		order = "e.size DESC"
	case "", "updated":
	default:
		return bad("排序方式不正确")
	}
	params := append(append([]any{}, args...), 50, (page-1)*50)
	rows, e := s.DB.Query("SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id"+where+" ORDER BY (e.kind='folder') DESC,"+order+" LIMIT ? OFFSET ?", params...)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []*Entry{}
	for rows.Next() {
		v, e := scanEntry(rows)
		if e != nil {
			return e
		}
		v.CanManage = canManage(u, v)
		v.CanApprove = canModerate(u, v)
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	ancestors := []map[string]string{}
	if p := q.Get("parent"); p != "" {
		for i := 0; p != "" && i < 100; i++ {
			v, e := s.getEntry(p, u)
			if e != nil {
				return e
			}
			if v.SpaceKey != key || v.Deleted || v.Kind != "folder" {
				return notfound()
			}
			ancestors = append([]map[string]string{{"id": v.ID, "name": v.Name}}, ancestors...)
			p = v.ParentID
		}
	}
	writeJSON(w, 200, map[string]any{"items": out, "total": total, "page": page, "page_size": 50, "ancestors": ancestors})
	return nil
}
func (s *Server) fileDetail(w http.ResponseWriter, r *http.Request, u *User) error {
	v, e := s.getEntry(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	writeJSON(w, 200, v)
	return nil
}
func validName(n string) bool {
	return n != "" && n != "." && n != ".." && strings.TrimSpace(n) == n && utf8.RuneCountInString(n) <= 255 && !strings.ContainsAny(n, "/\\\x00\r\n") && !strings.HasSuffix(n, ".")
}
func validateRemark(r string) error {
	if utf8.RuneCountInString(r) > 2000 {
		return bad("备注不能超过 2000 个字符")
	}
	return nil
}
func duplicate(e error) bool { var m *mysql.MySQLError; return errors.As(e, &m) && m.Number == 1062 }
func (s *Server) checkParent(parent, key string, u *User) error {
	if parent == "" {
		return nil
	}
	v, e := s.getEntry(parent, u)
	if e != nil {
		return e
	}
	if v.Kind != "folder" || v.Deleted || v.SpaceKey != key {
		return bad("目标目录不正确")
	}
	return nil
}
func publication(u *User, scope string) string {
	if scope == "personal" || operations(u) || scope == "department" && u.Role == "dept_admin" {
		return "published"
	}
	return "pending"
}
func (s *Server) createFolder(w http.ResponseWriter, r *http.Request, u *User) error {
	var p struct {
		Scope    string `json:"scope"`
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
		Remark   string `json:"remark"`
		OwnerID  string `json:"owner_id"`
		DeptID   string `json:"dept_id"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if !validName(p.Name) {
		return bad("目录名称不正确")
	}
	if e := validateRemark(p.Remark); e != nil {
		return e
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
	if p.ParentID != "" {
		var parentDeleted bool
		var parentSpace string
		if e = tx.QueryRow("SELECT deleted,space_key FROM entries WHERE id=? AND kind='folder' FOR UPDATE", p.ParentID).Scan(&parentDeleted, &parentSpace); e != nil {
			return bad("目标目录不可用")
		}
		if parentDeleted || parentSpace != key {
			return bad("目标目录不可用")
		}
	}
	id := newID()
	_, e = tx.Exec("INSERT INTO entries(id,parent_id,space_key,scope,owner_id,dept_id,kind,name,name_hash,remark,scan_status) VALUES(?,?,?,?,?,?,'folder',?,?,?,'clean')", id, p.ParentID, key, p.Scope, owner, dept, p.Name, digest(strings.ToLower(p.Name)), p.Remark)
	if duplicate(e) {
		return conflict("当前目录已有同名项目，请更换名称")
	}
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.audit(u.ID, "folder_create", id, "ok")
	writeJSON(w, 201, map[string]string{"id": id})
	return nil
}
func (s *Server) editFile(w http.ResponseWriter, r *http.Request, u *User) error {
	v, e := s.getEntry(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	if !canManage(u, v) || v.Deleted {
		return forbidden()
	}
	var p struct {
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if e = decode(w, r, &p); e != nil {
		return e
	}
	if !validName(p.Name) {
		return bad("文件名称不正确")
	}
	if e = validateRemark(p.Remark); e != nil {
		return e
	}
	pub := v.Publication
	if v.Kind == "file" && v.Scope != "personal" && !canModerate(u, v) {
		pub = "pending"
	}
	_, e = s.DB.Exec("UPDATE entries SET name=?,name_hash=?,remark=?,publication=?,updated_at=NOW(6) WHERE id=?", p.Name, digest(strings.ToLower(p.Name)), p.Remark, pub, v.ID)
	if duplicate(e) {
		return conflict("当前目录已有同名项目")
	}
	if e != nil {
		return e
	}
	s.audit(u.ID, "file_edit", v.ID, "ok")
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Server) trashFile(w http.ResponseWriter, r *http.Request, u *User) error {
	return s.setTrash(w, r, u, true)
}
func (s *Server) restoreFile(w http.ResponseWriter, r *http.Request, u *User) error {
	return s.setTrash(w, r, u, false)
}
func (s *Server) setTrash(w http.ResponseWriter, r *http.Request, u *User, deleted bool) error {
	v, e := s.getEntry(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	if !canManage(u, v) {
		return forbidden()
	}
	if !deleted && v.ParentID != "" {
		p, e := s.getEntry(v.ParentID, u)
		if e != nil {
			return e
		}
		if p.Deleted {
			return bad("请先恢复上级目录")
		}
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e = scanEntry(tx.QueryRow("SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.id=? FOR UPDATE", v.ID))
	if e != nil {
		return e
	}
	if !canManage(u, v) {
		return forbidden()
	}
	if v.Deleted == deleted {
		return conflict("文件状态已变化，请刷新后重试")
	}
	if !deleted && v.ParentID != "" {
		var parentDeleted bool
		if e = tx.QueryRow("SELECT deleted FROM entries WHERE id=? FOR UPDATE", v.ParentID).Scan(&parentDeleted); e != nil {
			return e
		}
		if parentDeleted {
			return bad("请先恢复上级目录")
		}
	}
	batch := v.TrashBatch
	if deleted {
		batch = newID()
	}
	ids := []string{v.ID}
	for i := 0; i < len(ids); i++ {
		rows, e := tx.Query("SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.parent_id=? FOR UPDATE", ids[i])
		if e != nil {
			return e
		}
		for rows.Next() {
			child, e := scanEntry(rows)
			if e != nil {
				rows.Close()
				return e
			}
			if deleted && child.Deleted {
				continue
			}
			if !deleted && (!child.Deleted || child.TrashBatch != batch) {
				continue
			}
			if !canManage(u, child) {
				rows.Close()
				return bad("目录内有您无权管理的文件，请由该空间管理员处理")
			}
			ids = append(ids, child.ID)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(ids) > 100000 {
			return bad("目录项目过多，请分批处理")
		}
	}
	for _, id := range ids {
		newBatch := batch
		if !deleted {
			newBatch = ""
		}
		if _, e = tx.Exec("UPDATE entries SET deleted=?,trash_batch=?,updated_at=NOW(6) WHERE id=?", deleted, newBatch, id); e != nil {
			return e
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	action := "file_restore"
	if deleted {
		action = "file_trash"
	}
	s.audit(u.ID, action, v.ID, "ok")
	writeJSON(w, 200, map[string]int{"count": len(ids)})
	return nil
}
func (s *Server) approveFile(w http.ResponseWriter, r *http.Request, u *User) error {
	v, e := s.getEntry(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	if !canModerate(u, v) || v.Deleted {
		return forbidden()
	}
	var p struct {
		Approve bool `json:"approve"`
	}
	if e = decode(w, r, &p); e != nil {
		return e
	}
	status := "rejected"
	if p.Approve {
		if v.ScanStatus != "clean" {
			return bad("通过病毒检查后才能发布")
		}
		status = "published"
	}
	if _, e = s.DB.Exec("UPDATE entries SET publication=?,updated_at=NOW(6) WHERE id=?", status, v.ID); e != nil {
		return e
	}
	s.audit(u.ID, "file_review", v.ID, status)
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Server) entryPath(v *Entry) (string, error) {
	if v.StorageKind == "legacy" {
		if s.Config.LegacyRoot == "" {
			return "", fmt.Errorf("历史资料目录尚未配置")
		}
		return safePath(s.Config.LegacyRoot, v.StoragePath)
	}
	if v.StorageKind != "local" {
		return "", fmt.Errorf("无效存储类型")
	}
	return safePath(filepath.Join(s.Config.DataDir, "objects"), v.StoragePath)
}
func inlineMIME(v string) bool {
	switch v {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "image/bmp", "application/pdf", "video/mp4", "video/webm", "audio/mpeg", "audio/wav", "audio/ogg", "audio/mp4":
		return true
	}
	return false
}
func (s *Server) content(w http.ResponseWriter, r *http.Request, u *User) error {
	v, e := s.getEntry(r.PathValue("id"), u)
	if e != nil {
		return e
	}
	if v.Deleted || v.Kind != "file" {
		return notfound()
	}
	if v.ScanStatus != "clean" || v.SourceMissing {
		return apiError{423, "文件尚未通过安全检查或源文件不可用"}
	}
	p, e := s.entryPath(v)
	if e != nil {
		return apiError{410, "源文件不可用，请联系管理员重新扫描"}
	}
	f, e := os.Open(p)
	if e != nil {
		return apiError{410, "源文件不可用"}
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return notfound()
	}
	if info.Size() != v.Size || info.ModTime().UnixNano() != v.SourceMtime {
		_, _ = s.DB.Exec("UPDATE entries SET scan_status='pending',scan_note='源文件已变更，等待重新检查' WHERE id=?", v.ID)
		return apiError{423, "文件发生变化，需重新安全检查"}
	}
	disposition := "attachment"
	if r.URL.Query().Get("preview") == "true" && inlineMIME(v.MIME) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": v.Name}))
	w.Header().Set("Content-Type", v.MIME)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	s.audit(u.ID, "file_content", v.ID, disposition)
	http.ServeContent(w, r, v.Name, info.ModTime(), f)
	return nil
}
func (s *Server) stats(w http.ResponseWriter, r *http.Request, u *User) error {
	out := map[string]any{}
	for _, scope := range []string{"company", "department", "personal"} {
		key, _, _, e := s.resolveSpace(u, scope, r.URL.Query().Get("owner"), r.URL.Query().Get("dept"))
		if e != nil {
			continue
		}
		condition := "space_key=? AND deleted=0 AND kind='file'"
		args := []any{key}
		if scope != "personal" && !operations(u) && !(scope == "department" && u.Role == "dept_admin") {
			condition += " AND (publication='published' OR owner_id=?)"
			args = append(args, u.ID)
		}
		var count, size int64
		if e = s.DB.QueryRow("SELECT COUNT(*),COALESCE(SUM(size),0) FROM entries WHERE "+condition, args...).Scan(&count, &size); e != nil {
			return e
		}
		out[scope] = map[string]int64{"count": count, "bytes": size}
	}
	var used int64
	if e := s.DB.QueryRow("SELECT COALESCE(SUM(size),0) FROM entries WHERE space_key=? AND kind='file'", "personal:"+u.ID).Scan(&used); e != nil {
		return e
	}
	out["personal_used"] = used
	out["personal_quota"] = u.QuotaBytes
	writeJSON(w, 200, out)
	return nil
}
