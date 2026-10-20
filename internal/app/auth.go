package app

import (
	"database/sql"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Mobile      string `json:"mobile"`
	Role        string `json:"role"`
	DeptID      string `json:"dept_id"`
	DeptName    string `json:"dept_name"`
	Enabled     bool   `json:"enabled"`
	QuotaBytes  int64  `json:"quota_bytes"`
	CSRF        string `json:"csrf,omitempty"`
}

const userFields = "u.id,u.username,u.display_name,COALESCE(u.mobile,''),u.role,u.dept_id,COALESCE(d.name,''),u.enabled,u.quota_bytes"

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (*User, error) {
	u := new(User)
	e := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Mobile, &u.Role, &u.DeptID, &u.DeptName, &u.Enabled, &u.QuotaBytes)
	return u, e
}

var mobilePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
var usernamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{2,31}$`)

func (s *Server) authenticate(r *http.Request) (*User, error) {
	cookie, e := r.Cookie("gmpan_session")
	if e != nil {
		return nil, apiError{401, "请先登录"}
	}
	var id, csrf string
	e = s.DB.QueryRow("SELECT user_id,csrf FROM sessions WHERE token_hash=? AND expires_at>NOW()", digest(cookie.Value)).Scan(&id, &csrf)
	if e == sql.ErrNoRows {
		return nil, apiError{401, "登录已失效，请重新登录"}
	}
	if e != nil {
		return nil, e
	}
	u, e := scanUser(s.DB.QueryRow("SELECT "+userFields+" FROM users u LEFT JOIN departments d ON d.id=u.dept_id WHERE u.id=? AND u.enabled=1", id))
	if e == sql.ErrNoRows {
		return nil, apiError{401, "账号已停用"}
	}
	if e != nil {
		return nil, e
	}
	u.CSRF = csrf
	return u, nil
}
func (s *Server) session(w http.ResponseWriter, u *User) error {
	token := newID() + newID()
	u.CSRF = newID() + newID()
	_, e := s.DB.Exec("INSERT INTO sessions(token_hash,user_id,csrf,expires_at) VALUES(?,?,?,?)", digest(token), u.ID, u.CSRF, time.Now().Add(12*time.Hour))
	if e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "gmpan_session", Value: token, Path: "/", HttpOnly: true, Secure: s.Config.TLSCert != "", SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	s.audit(u.ID, "login", "", "ok")
	writeJSON(w, 200, u)
	return nil
}
func (s *Server) login(w http.ResponseWriter, r *http.Request, _ *User) error {
	var p struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	p.Account = strings.TrimSpace(p.Account)
	if p.Account == "" || len(p.Account) > 64 || len(p.Password) > 72 {
		return bad("账号或密码不正确")
	}
	blocked, e := s.limited(strings.ToLower(p.Account), ipKey(r), "password", 10, 60)
	if e != nil {
		return e
	}
	if blocked {
		return apiError{429, "尝试过于频繁，请稍后再试"}
	}
	var hash, id string
	e = s.DB.QueryRow("SELECT id,password_hash FROM users WHERE (username=? OR mobile=?) AND enabled=1 LIMIT 1", p.Account, p.Account).Scan(&id, &hash)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if hash == "" {
		hash = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5gW8OZ9J8N7EhkMpT3uQ6MgeCv/jLeq"
	}
	check := bcrypt.CompareHashAndPassword([]byte(hash), []byte(p.Password))
	if e == sql.ErrNoRows || check != nil {
		s.attempt(strings.ToLower(p.Account), ipKey(r), "password")
		return apiError{401, "账号或密码不正确"}
	}
	u, e := scanUser(s.DB.QueryRow("SELECT "+userFields+" FROM users u LEFT JOIN departments d ON d.id=u.dept_id WHERE u.id=?", id))
	if e != nil {
		return e
	}
	return s.session(w, u)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, u *User) error {
	cookie, _ := r.Cookie("gmpan_session")
	if _, e := s.DB.Exec("DELETE FROM sessions WHERE token_hash=?", digest(cookie.Value)); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "gmpan_session", Value: "", Path: "/", HttpOnly: true, Secure: s.Config.TLSCert != "", SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func validPassword(p string) bool { return len(p) >= 10 && len(p) <= 72 }
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *User) error {
	var p struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if !validPassword(p.New) {
		return bad("新密码需为 10～72 字节")
	}
	var hash string
	if e := s.DB.QueryRow("SELECT password_hash FROM users WHERE id=?", u.ID).Scan(&hash); e != nil {
		return e
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(p.Old)) != nil {
		return bad("当前密码不正确")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(p.New), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE users SET password_hash=? WHERE id=?", string(b), u.ID); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", u.ID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.audit(u.ID, "password_change", u.ID, "ok")
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func CreateAdmin(db *sql.DB, username, password string, quota int64) error {
	if !usernamePattern.MatchString(username) {
		return bad("用户名需为字母开头的 3～32 位字母、数字或 _.-")
	}
	if !validPassword(password) {
		return bad("密码需为 10～72 字节")
	}
	var n int
	if e := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return conflict("已有员工账号，请通过管理页面新增管理员")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	_, e = db.Exec("INSERT INTO users(id,username,display_name,password_hash,role,quota_bytes) VALUES(?,?,?,?,?,?)", newID(), username, "系统管理员", string(hash), "admin", quota)
	return e
}
