package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Scanner interface {
	Check(context.Context, string) (string, string)
}
type CommandScanner struct {
	Path        string
	DatabaseDir string
}

func (c CommandScanner) Check(ctx context.Context, path string) (string, string) {
	args := []string{"--no-summary", "--stdout", "--alert-encrypted=yes", "--alert-exceeds-max=yes", "--max-filesize=2048M", "--max-scansize=4096M", "--max-recursion=20", "--max-files=10000"}
	if c.DatabaseDir != "" {
		args = append(args, "--database="+c.DatabaseDir)
	}
	args = append(args, "--", path)
	cmd := exec.CommandContext(ctx, c.Path, args...)
	if e := cmd.Run(); e != nil {
		var exit *exec.ExitError
		if errors.As(e, &exit) && exit.ExitCode() == 1 {
			return "infected", "发现威胁、加密内容或超过扫描限制，已阻止访问"
		}
		return "failed", "病毒检查未完成，请联系管理员检查扫描程序和病毒库"
	}
	return "clean", ""
}
func (s *Server) StartWorkers(ctx context.Context) {
	_, _ = s.DB.Exec("UPDATE import_jobs SET status='interrupted',error_note='服务重启，可重新扫描',finished_at=NOW(6) WHERE status='running'")
	_, _ = s.DB.Exec("UPDATE entries SET scan_status='pending' WHERE scan_status='scanning'")
	s.workers.Add(2)
	go func() {
		defer s.workers.Done()
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if s.scan != nil {
					s.scanBatch(ctx)
				}
			}
		}
	}()
	go func() {
		defer s.workers.Done()
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.cleanup()
			}
		}
	}()
}
func (s *Server) WaitWorkers() { s.workers.Wait() }

func (s *Server) scanBatch(ctx context.Context) {
	if allowed, _ := s.scanSettings().window(time.Now()); !allowed {
		return
	}
	rows, e := s.DB.Query("SELECT id FROM entries WHERE kind='file' AND scan_status='pending' AND source_missing=0 AND deleted=0 ORDER BY created_at LIMIT 10")
	if e != nil {
		s.Log.Error("scan queue unavailable")
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.scanOne(ctx, id)
	}
}
func (s *Server) scanOne(ctx context.Context, id string) {
	s.scanWorkMu.Lock()
	defer s.scanWorkMu.Unlock()
	taskCtx, finish, allowed := s.scanTaskContext(ctx)
	if !allowed {
		return
	}
	defer finish()
	ctx = taskCtx
	result, e := s.DB.Exec("UPDATE entries SET scan_status='scanning' WHERE id=? AND scan_status='pending'", id)
	if e != nil {
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return
	}
	v, e := scanEntry(s.DB.QueryRow("SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.id=?", id))
	if e != nil {
		return
	}
	status, note := "failed", "源文件不可读取"
	hash, mimeType := "", v.MIME
	p, e := s.entryPath(v)
	if e == nil {
		f, err := os.Open(p)
		if err == nil {
			stopClose := context.AfterFunc(ctx, func() { _ = f.Close() })
			defer stopClose()
			before, err := f.Stat()
			if err == nil && before.Mode().IsRegular() {
				header := make([]byte, 512)
				n, _ := f.Read(header)
				mimeType = http.DetectContentType(header[:n])
				_, _ = f.Seek(0, 0)
				h := sha256.New()
				_, err = io.Copy(h, f)
				if err == nil {
					hash = hex.EncodeToString(h.Sum(nil))
					scanCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
					status, note = s.scan.Check(scanCtx, p)
					cancel()
					after, statErr := os.Stat(p)
					if statErr != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || before.Size() != v.Size || before.ModTime().UnixNano() != v.SourceMtime {
						status = "failed"
						note = "检查期间文件发生变化，请重新扫描资料目录"
					}
				}
			}
			f.Close()
		}
	}
	if ctx.Err() != nil {
		status = "pending"
		note = "检查已暂停，等待下次检查时间或设置更新"
	}
	_, e = s.DB.Exec("UPDATE entries SET scan_status=?,scan_note=?,sha256=?,mime=? WHERE id=? AND scan_status='scanning'", status, note, hash, mimeType, id)
	if e != nil {
		s.Log.Error("scan result write failed", "entry_id", id)
	}
	s.audit("", "file_scan", id, status)
}
func (s *Server) cleanup() {
	_, _ = s.DB.Exec("DELETE FROM sessions WHERE expires_at<NOW()")
	_, _ = s.DB.Exec("DELETE FROM auth_attempts WHERE created_at<DATE_SUB(NOW(),INTERVAL 1 DAY)")
	rows, e := s.DB.Query("SELECT id FROM uploads WHERE expires_at<NOW() AND status IN ('uploading','cancelled','completed','expired')")
	if e != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if len(id) != 32 || strings.ContainsAny(id, "/\\") {
			continue
		}
		p := filepath.Join(s.Config.DataDir, "chunks", id)
		if !within(filepath.Join(s.Config.DataDir, "chunks"), p) {
			continue
		}
		if e = os.RemoveAll(p); e == nil {
			_, _ = s.DB.Exec("UPDATE uploads SET status='expired' WHERE id=? AND status='uploading'", id)
			_, _ = s.DB.Exec("DELETE FROM upload_chunks WHERE upload_id=?", id)
		}
	}
}
