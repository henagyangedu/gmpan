package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

func decodeSelection(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var p struct {
		IDs []string `json:"ids"`
	}
	if e := decode(w, r, &p); e != nil {
		return nil, e
	}
	if len(p.IDs) == 0 || len(p.IDs) > folderMaxFiles {
		return nil, bad("每次请选择 1～500 个同目录项目")
	}
	seen := map[string]bool{}
	for _, id := range p.IDs {
		if id == "" || seen[id] {
			return nil, bad("选择项目为空或重复，请刷新后重试")
		}
		seen[id] = true
	}
	return p.IDs, nil
}
func selectionEntries(rows *sql.Rows, u *User, count int) ([]*Entry, error) {
	var entries []*Entry
	for rows.Next() {
		v, e := scanEntry(rows)
		if e != nil {
			return nil, e
		}
		if !canRead(u, v) {
			return nil, notfound()
		}
		entries = append(entries, v)
	}
	if e := rows.Err(); e != nil {
		return nil, e
	}
	if len(entries) != count {
		return nil, notfound()
	}
	for _, v := range entries {
		if v.Deleted {
			return nil, conflict("选中项目状态已变化，请刷新")
		}
		if v.ParentID != entries[0].ParentID || v.SpaceKey != entries[0].SpaceKey {
			return nil, bad("批量操作只支持同一目录内的项目")
		}
	}
	return entries, nil
}

func (s *Server) batchTrashFiles(w http.ResponseWriter, r *http.Request, u *User) error {
	ids, e := decodeSelection(w, r)
	if e != nil {
		return e
	}
	tx, e := s.DB.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, e := tx.QueryContext(r.Context(), "SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.id IN ("+placeholders(len(ids))+") ORDER BY e.id FOR UPDATE", args...)
	if e != nil {
		return e
	}
	roots, e := selectionEntries(rows, u, len(ids))
	rows.Close()
	if e != nil {
		return e
	}
	all := append([]*Entry{}, roots...)
	for i := 0; i < len(all); i++ {
		v := all[i]
		if !canManage(u, v) {
			return bad("选中目录内含无权管理的项目，未删除任何项目")
		}
		if v.Kind != "folder" {
			continue
		}
		children, e := tx.QueryContext(r.Context(), "SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.space_key=? AND e.parent_id=? AND e.deleted=0 ORDER BY e.id LIMIT 10001 FOR UPDATE", v.SpaceKey, v.ID)
		if e != nil {
			return e
		}
		for children.Next() {
			child, e := scanEntry(children)
			if e != nil {
				children.Close()
				return e
			}
			all = append(all, child)
			if len(all) > 10000 {
				children.Close()
				return bad("含子目录最多批量处理 10000 项，请分批删除")
			}
		}
		e = children.Err()
		children.Close()
		if e != nil {
			return e
		}
	}
	batch := newID()
	values := []any{batch}
	for _, entry := range all {
		values = append(values, entry.ID)
	}
	if _, e = tx.ExecContext(r.Context(), "UPDATE entries SET deleted=1,trash_batch=?,updated_at=NOW(6) WHERE id IN ("+placeholders(len(all))+")", values...); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.audit(u.ID, "file_batch_trash", roots[0].ID, fmt.Sprintf("selected=%d,total=%d", len(roots), len(all)))
	writeJSON(w, 200, map[string]int{"selected": len(roots), "count": len(all)})
	return nil
}
func (s *Server) prepareBatchDownload(w http.ResponseWriter, r *http.Request, u *User) error {
	ids, e := decodeSelection(w, r)
	if e != nil {
		return e
	}
	return s.prepareArchive(w, r, u, "file_batch_archive", func(ctx context.Context) ([]archiveItem, int64, string, error) {
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, e := s.DB.QueryContext(ctx, "SELECT "+entryFields+" FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE e.id IN ("+placeholders(len(ids))+") ORDER BY e.id", args...)
		if e != nil {
			return nil, 0, "", e
		}
		entries, e := selectionEntries(rows, u, len(ids))
		rows.Close()
		if e != nil {
			return nil, 0, "", e
		}
		var items []archiveItem
		var total int64
		for _, v := range entries {
			if v.Kind != "file" {
				return nil, 0, "", bad("批量下载只支持文件；目录请使用单个目录下载")
			}
			if !validName(v.Name) || strings.Contains(v.Name, ":") {
				return nil, 0, "", bad("文件名不适合打包，请先重命名")
			}
			if !s.scanAccess(v) {
				return nil, 0, "", apiError{423, "选中文件尚未通过安全检查或源文件不可用"}
			}
			if v.Size < 0 || v.Size > folderMaxBytes-total {
				return nil, 0, "", bad("批量下载最多 2 GiB，请分批选择")
			}
			total += v.Size
			items = append(items, archiveItem{v, v.Name, 0})
		}
		return items, total, "批量文件.zip", nil
	})
}
