package app

import (
	"net/http"
	"strings"
)

// Company directory browsing uses the same publication rules as the file list.
func companyPublication(u *User) (string, []any) {
	if operations(u) {
		return "", nil
	}
	return " AND (publication='published' OR owner_id=?)", []any{u.ID}
}
func (s *Server) companyFolders(w http.ResponseWriter, r *http.Request, u *User) error {
	parent := r.URL.Query().Get("parent")
	if e := s.checkParent(parent, "company", u); e != nil {
		return e
	}
	extra, args := companyPublication(u)
	args = append([]any{parent}, args...)
	rows, e := s.DB.Query("SELECT id,name FROM entries WHERE space_key='company' AND kind='folder' AND deleted=0 AND parent_id=?"+extra+" ORDER BY name", args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name string
		if e = rows.Scan(&id, &name); e != nil {
			return e
		}
		out = append(out, map[string]string{"id": id, "name": name})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Server) companyDescendants(parent string, u *User) ([]string, error) {
	extra, args := companyPublication(u)
	rows, e := s.DB.Query("SELECT id,parent_id FROM entries WHERE space_key='company' AND kind='folder' AND deleted=0"+extra+" LIMIT 20001", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	children := map[string][]string{}
	n := 0
	for rows.Next() {
		var id, p string
		if e = rows.Scan(&id, &p); e != nil {
			return nil, e
		}
		children[p] = append(children[p], id)
		n++
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if n > 20000 {
		return nil, bad("目录数量较多，请进入具体目录后浏览，或关闭包含子目录")
	}
	ids := []string{parent}
	seen := map[string]bool{parent: true}
	for i := 0; i < len(ids); i++ {
		for _, id := range children[ids[i]] {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
