package app

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type ImportJob struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Added       int64      `json:"added"`
	Updated     int64      `json:"updated"`
	Skipped     int64      `json:"skipped"`
	Errors      int64      `json:"errors"`
	CurrentPath string     `json:"current_path"`
	ErrorNote   string     `json:"error_note"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

func (s *Server) imports(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	rows, e := s.DB.Query("SELECT id,status,added,updated,skipped,errors,current_path,error_note,created_at,finished_at FROM import_jobs ORDER BY created_at DESC LIMIT 20")
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []ImportJob{}
	for rows.Next() {
		var j ImportJob
		if e = rows.Scan(&j.ID, &j.Status, &j.Added, &j.Updated, &j.Skipped, &j.Errors, &j.CurrentPath, &j.ErrorNote, &j.CreatedAt, &j.FinishedAt); e != nil {
			return e
		}
		out = append(out, j)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
func canonicalRoot(root string) (string, error) {
	r, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", e
	}
	r, e = filepath.Abs(r)
	if e != nil {
		return "", e
	}
	info, e := os.Stat(r)
	if e != nil {
		return "", e
	}
	if !info.IsDir() {
		return "", bad("历史资料根路径必须是目录")
	}
	return filepath.Clean(r), nil
}
func sourceHash(root, relative string) string {
	p := filepath.ToSlash(root) + "\x00" + filepath.ToSlash(relative)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return digest(p)
}
func (s *Server) startImport(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	if s.Config.LegacyRoot == "" {
		return bad("请在部署配置中设置历史资料根目录并重启服务")
	}
	root, e := canonicalRoot(s.Config.LegacyRoot)
	if e != nil {
		return bad("历史资料目录不存在或无法读取")
	}
	for _, protected := range []string{s.Config.DataDir, s.Config.LogDir} {
		realProtected, err := filepath.EvalSymlinks(protected)
		if err != nil {
			return bad("系统存储目录不可用")
		}
		if s.Config.StorageRoot != "" && sameDirectory(realProtected, s.Config.DataDir) {
			continue
		}
		if within(root, realProtected) || within(realProtected, root) {
			return bad("历史资料目录不能与系统数据或日志目录重叠")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.directoryChangePending(false) {
		return conflict("读取目录等待生效，请重启服务后再扫描")
	}
	var running int
	if e = s.DB.QueryRow("SELECT COUNT(*) FROM import_jobs WHERE status='running'").Scan(&running); e != nil {
		return e
	}
	if running > 0 {
		return conflict("已有扫描任务运行中")
	}
	var foreign int
	rootHash := sourceHash(root, "")
	if e = s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE storage_kind='legacy' AND source_root_hash<>?", rootHash).Scan(&foreign); e != nil {
		return e
	}
	if foreign > 0 {
		return bad("已有历史资料属于另一个根目录；请恢复原配置，迁移目录需先核对索引")
	}
	id := newID()
	if _, e = s.DB.Exec("INSERT INTO import_jobs(id,root_hash,user_id) VALUES(?,?,?)", id, rootHash, u.ID); e != nil {
		return e
	}
	s.audit(u.ID, "import_start", id, "ok")
	go s.runImport(context.Background(), id, root, u.ID)
	writeJSON(w, 202, map[string]string{"id": id})
	return nil
}
func (s *Server) runImport(ctx context.Context, id, root, owner string) {
	var added, updated, skipped, failures int64
	note := ""
	current := ""
	rootHash := sourceHash(root, "")
	persist := func() {
		_, e := s.DB.Exec("UPDATE import_jobs SET added=?,updated=?,skipped=?,errors=?,current_path=?,error_note=? WHERE id=?", added, updated, skipped, failures, current, note, id)
		if e != nil {
			s.Log.Error("import progress write failed", "job_id", id)
		}
	}
	// Descendants are resolved by their source keys, so display renames do not alter the import hierarchy.
	rootID, change, e := s.upsertImported(id, rootHash, sourceHash(root, "."), "", "历史资料", ".", owner, nil)
	if e != nil {
		note = "历史资料目录登记失败，请检查重名项目及日志"
		failures++
		persist()
		_, _ = s.DB.Exec("UPDATE import_jobs SET status='failed',finished_at=NOW(6) WHERE id=?", id)
		s.Log.Error("import root failed", "job_id", id, "error", e.Error())
		return
	}
	if change == "added" {
		added++
	}
	dirIDs := map[string]string{".": rootID}
	walkError := false
	lastPersist := time.Now()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if relative == "." {
			return nil
		}
		current = filepath.ToSlash(relative)
		if walkErr != nil {
			failures++
			walkError = true
			note = "部分路径无法读取；源文件缺失标记未执行"
			return nil
		}
		if (strings.EqualFold(d.Name(), ".gmpan") && filepath.Dir(relative) == ".") || d.Name() == ".git" || d.Name() == ".svn" || d.Name() == "$RECYCLE.BIN" || d.Name() == "System Volume Information" {
			skipped++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil || d.Type()&os.ModeSymlink != 0 || !within(root, real) || filepath.Clean(real) != filepath.Clean(path) {
			skipped++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, e := d.Info()
		if e != nil {
			failures++
			walkError = true
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			skipped++
			return nil
		}
		parent, ok := dirIDs[filepath.Dir(relative)]
		if !ok {
			failures++
			walkError = true
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entryID, change, e := s.upsertImported(id, rootHash, sourceHash(root, relative), parent, d.Name(), filepath.ToSlash(relative), owner, info)
		if e != nil {
			failures++
			walkError = true
			note = "部分项目登记失败，请检查重名或访问权限"
			s.Log.Error("import entry failed", "job_id", id, "error", e.Error())
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			dirIDs[relative] = entryID
		}
		switch change {
		case "added":
			added++
		case "updated":
			updated++
		default:
			skipped++
		}
		if time.Since(lastPersist) > time.Second {
			persist()
			lastPersist = time.Now()
		}
		return nil
	})
	status := "completed"
	if err != nil {
		status = "interrupted"
		note = "扫描中断，可以重新运行"
		failures++
	} else if walkError {
		status = "partial"
	} else {
		if _, e = s.DB.Exec("UPDATE entries SET source_missing=1,scan_note='源文件未找到' WHERE storage_kind='legacy' AND source_root_hash=? AND import_seen<>?", rootHash, id); e != nil {
			status = "failed"
			note = "缺失文件状态更新失败"
			failures++
		}
	}
	persist()
	_, e = s.DB.Exec("UPDATE import_jobs SET status=?,current_path='',finished_at=NOW(6) WHERE id=?", status, id)
	if e != nil {
		s.Log.Error("import completion write failed", "job_id", id)
	}
	s.audit(owner, "import_finish", id, status)
}
func (s *Server) upsertImported(jobID, rootHash, key, parent, name, relative, owner string, info fs.FileInfo) (string, string, error) {
	kind := "folder"
	var size, mtime int64
	status := "clean"
	if info != nil && !info.IsDir() {
		kind = "file"
		size = info.Size()
		mtime = info.ModTime().UnixNano()
		status = "pending"
	}
	var existing string
	var oldSize, oldMtime int64
	var missing bool
	e := s.DB.QueryRow("SELECT id,size,source_mtime,source_missing FROM entries WHERE source_key=?", key).Scan(&existing, &oldSize, &oldMtime, &missing)
	if e == nil {
		changed := oldSize != size || oldMtime != mtime || missing
		if changed {
			_, e = s.DB.Exec("UPDATE entries SET size=?,source_mtime=?,scan_status=?,scan_note='',sha256='',source_missing=0,import_seen=?,updated_at=NOW(6) WHERE id=?", size, mtime, status, jobID, existing)
			return existing, "updated", e
		}
		_, e = s.DB.Exec("UPDATE entries SET import_seen=? WHERE id=?", jobID, existing)
		return existing, "skipped", e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", "", e
	}
	id := newID()
	_, e = s.DB.Exec("INSERT INTO entries(id,parent_id,space_key,scope,owner_id,kind,name,name_hash,remark,size,mime,storage_kind,storage_path,source_key,source_root_hash,import_seen,scan_status,source_mtime,publication) VALUES(?,?,'company','company',?,?,?,?,?,?,'application/octet-stream','legacy',?,?,?,?,?,?,'published')", id, parent, owner, kind, name, digest(strings.ToLower(name)), "", size, relative, key, rootHash, jobID, status, mtime)
	return id, "added", e
}
