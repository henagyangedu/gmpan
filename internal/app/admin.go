package app

import (
	"database/sql"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

func admin(u *User) error {
	if !operations(u) {
		return forbidden()
	}
	return nil
}
func operations(u *User) bool { return u != nil && (u.Role == "admin" || u.Role == "maintainer") }
func superAdmin(u *User) error {
	if u == nil || u.Role != "admin" {
		return forbidden()
	}
	return nil
}
func (s *Server) departments(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.DB.Query("SELECT id,name,parent_id FROM departments ORDER BY name")
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, n, p string
		if e = rows.Scan(&id, &n, &p); e != nil {
			return e
		}
		out = append(out, map[string]string{"id": id, "name": n, "parent_id": p})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Server) createDepartment(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	var p struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 100 {
		return bad("部门名称需为 1～100 个字符")
	}
	if p.ParentID != "" {
		var n int
		if e := s.DB.QueryRow("SELECT COUNT(*) FROM departments WHERE id=?", p.ParentID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return bad("上级部门不存在")
		}
	}
	id := newID()
	if _, e := s.DB.Exec("INSERT INTO departments(id,name,parent_id) VALUES(?,?,?)", id, p.Name, p.ParentID); e != nil {
		return e
	}
	s.audit(u.ID, "department_create", id, "ok")
	writeJSON(w, 201, map[string]string{"id": id})
	return nil
}
func (s *Server) users(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	rows, e := s.DB.Query("SELECT " + userFields + " FROM users u LEFT JOIN departments d ON d.id=u.dept_id ORDER BY u.created_at")
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		v, e := scanUser(rows)
		if e != nil {
			return e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}

type userInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Mobile      string `json:"mobile"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	DeptID      string `json:"dept_id"`
	Enabled     bool   `json:"enabled"`
	QuotaBytes  int64  `json:"quota_bytes"`
}

func (s *Server) validateUser(p *userInput) error {
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if !usernamePattern.MatchString(p.Username) {
		return bad("用户名需为字母开头的 3～32 位字母、数字或 _.-")
	}
	if p.DisplayName == "" || utf8.RuneCountInString(p.DisplayName) > 100 {
		return bad("请输入 1～100 个字符的员工姓名")
	}
	if p.Mobile != "" && !mobilePattern.MatchString(p.Mobile) {
		return bad("手机号格式不正确")
	}
	if p.Role != "admin" && p.Role != "maintainer" && p.Role != "dept_admin" && p.Role != "member" {
		return bad("角色不正确")
	}
	if p.Role == "dept_admin" && p.DeptID == "" {
		return bad("部门管理员必须设置部门")
	}
	if p.QuotaBytes <= 0 {
		return bad("个人配额必须大于零")
	}
	if p.DeptID != "" {
		var n int
		if e := s.DB.QueryRow("SELECT COUNT(*) FROM departments WHERE id=?", p.DeptID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return bad("部门不存在")
		}
	}
	return nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	var p userInput
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if e := s.validateUser(&p); e != nil {
		return e
	}
	if u.Role != "admin" && p.Role == "admin" {
		return forbidden()
	}
	if !validPassword(p.Password) {
		return bad("初始密码需为 10～72 字节")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(p.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	id := newID()
	_, e = s.DB.Exec("INSERT INTO users(id,username,display_name,mobile,password_hash,role,dept_id,enabled,quota_bytes) VALUES(?,?,?,?,?,?,?,?,?)", id, p.Username, p.DisplayName, nullable(p.Mobile), string(hash), p.Role, p.DeptID, p.Enabled, p.QuotaBytes)
	if e != nil {
		if duplicate(e) {
			return conflict("用户名或手机号已被使用")
		}
		return e
	}
	s.audit(u.ID, "user_create", id, "ok")
	writeJSON(w, 201, map[string]string{"id": id})
	return nil
}
func (s *Server) editUser(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	var p userInput
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if e := s.validateUser(&p); e != nil {
		return e
	}
	id := r.PathValue("id")
	if id == u.ID && (!p.Enabled || p.Role != u.Role) {
		return bad("不能停用自己或更改自己的管理员角色")
	}
	if p.Password != "" && !validPassword(p.Password) {
		return bad("新密码需为 10～72 字节")
	}
	hash := ""
	if p.Password != "" {
		b, e := bcrypt.GenerateFromPassword([]byte(p.Password), bcrypt.DefaultCost)
		if e != nil {
			return e
		}
		hash = string(b)
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var role string
	var enabled bool
	if e = tx.QueryRow("SELECT role,enabled FROM users WHERE id=? FOR UPDATE", u.ID).Scan(&role, &enabled); e != nil {
		return e
	}
	if (role != "admin" && role != "maintainer") || !enabled {
		return forbidden()
	}
	var targetRole string
	if e = tx.QueryRow("SELECT role FROM users WHERE id=? FOR UPDATE", id).Scan(&targetRole); e == sql.ErrNoRows {
		return notfound()
	}
	if e != nil {
		return e
	}
	if role != "admin" && (p.Role == "admin" || targetRole == "admin") {
		return forbidden()
	}
	_, e = tx.Exec("UPDATE users SET username=?,display_name=?,mobile=?,role=?,dept_id=?,enabled=?,quota_bytes=?,password_hash=IF(?='',password_hash,?) WHERE id=?", p.Username, p.DisplayName, nullable(p.Mobile), p.Role, p.DeptID, p.Enabled, p.QuotaBytes, hash, hash, id)
	if e != nil {
		if duplicate(e) {
			return conflict("用户名或手机号已被使用")
		}
		return e
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", id); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.audit(u.ID, "user_update", id, "ok")
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Server) systemInfo(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	var pending, failed, infected int
	s.siteMu.RLock()
	companyName := s.site.CompanyName
	s.siteMu.RUnlock()
	if e := s.DB.QueryRow("SELECT COALESCE(SUM(scan_status='pending'),0),COALESCE(SUM(scan_status='failed'),0),COALESCE(SUM(scan_status='infected'),0) FROM entries WHERE kind='file' AND deleted=0").Scan(&pending, &failed, &infected); e != nil {
		return e
	}
	policy := s.scanSettings()
	active, _ := policy.window(time.Now())
	writeJSON(w, 200, map[string]any{"company_name": companyName, "legacy_configured": s.Config.LegacyRoot != "", "legacy_root": s.Config.LegacyRoot, "scanner_configured": s.scan != nil, "sms_enabled": s.smsEnabled(), "max_file_bytes": s.Config.MaxFileBytes, "chunk_size": ChunkSize, "pending_scans": pending, "failed_scans": failed, "infected_files": infected, "virus_scan": policy, "scan_active": active && s.scan != nil})
	return nil
}
func (s *Server) auditList(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	rows, e := s.DB.Query("SELECT a.id,COALESCE(u.display_name,'系统'),a.action,a.target_id,a.result,a.created_at FROM audit_logs a LEFT JOIN users u ON u.id=a.user_id ORDER BY a.id DESC LIMIT 200")
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, action, target, result string
		var at time.Time
		if e = rows.Scan(&id, &name, &action, &target, &result, &at); e != nil {
			return e
		}
		out = append(out, map[string]any{"id": id, "user": name, "action": action, "target_id": target, "result": result, "created_at": at})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Server) retryScans(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := admin(u); e != nil {
		return e
	}
	if s.scan == nil {
		return bad("请先配置病毒扫描程序并重启服务")
	}
	if s.scanSettings().Mode == "off" {
		return bad("病毒检查已关闭，请由超管启用后再重试")
	}
	result, e := s.DB.Exec("UPDATE entries SET scan_status='pending',scan_note='' WHERE scan_status='failed' AND kind='file'")
	if e != nil {
		return e
	}
	n, _ := result.RowsAffected()
	s.audit(u.ID, "scan_retry", "", "ok")
	writeJSON(w, 200, map[string]int64{"count": n})
	return nil
}
