package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

func TestVisibilityRules(t *testing.T) {
	owner := &User{ID: "alice", DeptID: "sales", Role: "member", Enabled: true}
	admin := &User{ID: "admin", DeptID: "other", Role: "admin", Enabled: true}
	cases := []struct {
		name         string
		u            *User
		file         Entry
		read, manage bool
	}{
		{"owner personal", owner, Entry{Scope: "personal", OwnerID: "alice", Publication: "published"}, true, true},
		{"admin manages personal", admin, Entry{Scope: "personal", OwnerID: "alice", Publication: "published"}, true, true},
		{"same department", owner, Entry{Scope: "department", DeptID: "sales", OwnerID: "bob", Publication: "published"}, true, false},
		{"admin manages other department", admin, Entry{Scope: "department", DeptID: "sales", OwnerID: "bob", Publication: "published"}, true, true},
		{"department owner after transfer", owner, Entry{Scope: "department", DeptID: "old", OwnerID: "alice", Publication: "published"}, false, false},
		{"company published", owner, Entry{Scope: "company", OwnerID: "bob", Publication: "published"}, true, false},
		{"company pending hidden", owner, Entry{Scope: "company", OwnerID: "bob", Publication: "pending"}, false, false},
		{"admin company pending", admin, Entry{Scope: "company", OwnerID: "bob", Publication: "pending"}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canRead(c.u, &c.file); got != c.read {
				t.Fatalf("read=%v", got)
			}
			if got := canManage(c.u, &c.file); got != c.manage {
				t.Fatalf("manage=%v", got)
			}
		})
	}
}
func TestStorageBoundaryAndHeaders(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := safePath(root, "../outside"); e == nil {
		t.Fatal("traversal accepted")
	}
	if _, e := safePath(root, "a.txt"); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(root, "escape")); e == nil {
		os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("private"), 0600)
		if _, e = safePath(root, "escape/secret.txt"); e == nil {
			t.Fatal("symlink escape accepted")
		}
	}
	if inlineMIME("image/svg+xml") || inlineMIME("text/html") {
		t.Fatal("active content may not be inline")
	}
	if validateHeader("photo.png", []byte("MZ executable")) {
		t.Fatal("spoofed extension accepted")
	}
	if !validateHeader("a.txt", []byte("中文说明")) {
		t.Fatal("UTF8 text rejected")
	}
}
func TestSMSWireProtocol(t *testing.T) {
	if got := SMSSign("test-key", "test-secret", "1730000000", "0123456789abcdef0123456789abcdef", []byte(`{"templateKey":"standard_code","mobile":"13800000000","variables":{"code":"123456"},"requestId":"mock-request-001"}`)); got != "751470e35f3697a23069c411d77e78a8ae1590e7c588a0d2afd9814451281a0f" {
		t.Fatalf("HMAC golden vector mismatch: %s", got)
	}
	count := 0
	response := `{"code":0}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		body, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Gm-Nonce")
		if len(nonce) != 32 {
			t.Errorf("nonce length=%d", len(nonce))
		}
		if r.Header.Get("X-Gm-Sign") != SMSSign("test-key", "test-secret", r.Header.Get("X-Gm-Timestamp"), nonce, body) {
			t.Error("body signature mismatch")
		}
		var payload struct {
			TemplateKey string            `json:"templateKey"`
			Mobile      string            `json:"mobile"`
			Variables   map[string]string `json:"variables"`
			RequestID   string            `json:"requestId"`
		}
		if r.URL.Path != "/sms/send" || json.Unmarshal(body, &payload) != nil || payload.TemplateKey != "standard_code" || payload.Mobile != "13800000000" || payload.Variables["code"] != "123456" || payload.RequestID != "mock-request-001" {
			t.Error("invalid body")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, response)
	}))
	defer server.Close()
	s := &Server{smsSettings: SMSSettings{SMSConfig: SMSConfig{BaseURL: server.URL, AppKey: "test-key", AppSecret: "test-secret"}, Revision: 1}, smsClient: server.Client()}
	config := SMSConfig{BaseURL: server.URL, AppKey: "test-key", AppSecret: "test-secret", TemplateKey: "standard_code"}
	if e := s.sendTemplateCode(config, "13800000000", "123456", "mock-request-001"); e != nil {
		t.Fatal(e)
	}
	response = `{}`
	if e := s.sendTemplateCode(config, "13800000000", "123456", "mock-request-001"); e == nil {
		t.Fatal("missing code treated as success")
	}
	response = `{"code":20003}`
	if e := s.sendTemplateCode(config, "13800000000", "123456", "mock-request-001"); e == nil {
		t.Fatal("business error treated as success")
	}
	if count != 3 {
		t.Fatal("unexpected retries")
	}
}

type testClient struct {
	cookie *http.Cookie
	csrf   string
	user   *User
}

func testServer(t *testing.T) (*Server, map[string]testClient) {
	t.Helper()
	dsn := os.Getenv("GMPAN_TEST_DSN")
	if dsn == "" {
		t.Skip("set GMPAN_TEST_DSN for isolated MySQL integration tests")
	}
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Addr != "127.0.0.1:13386" || cfg.DBName != "" {
		t.Fatal("integration tests only accept the isolated 127.0.0.1:13386 instance with an empty database name")
	}
	root, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "gmpan_test_" + newID()[:12]
	if _, e = root.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); e != nil {
		t.Fatal(e)
	}
	cfg.DBName = name
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); root.Exec("DROP DATABASE " + name); root.Close() })
	if e = Migrate(db); e != nil {
		t.Fatal(e)
	}
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	os.MkdirAll(legacy, 0700)
	c := Config{DataDir: filepath.Join(base, "data"), LogDir: filepath.Join(base, "logs"), LegacyRoot: legacy, MaxFileBytes: 10 << 30, PersonalQuotaBytes: 20 << 30}
	s, e := NewServer(db, c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	for _, dept := range []string{"sales", "finance"} {
		if _, e = db.Exec("INSERT INTO departments(id,name) VALUES(?,?)", dept, dept); e != nil {
			t.Fatal(e)
		}
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("testPassword2026"), bcrypt.MinCost)
	clients := map[string]testClient{}
	for _, item := range []struct{ id, role, dept string }{{"admin", "admin", "finance"}, {"maint", "maintainer", "finance"}, {"alice", "member", "sales"}, {"bob", "member", "finance"}, {"manager", "dept_admin", "sales"}} {
		if _, e = db.Exec("INSERT INTO users(id,username,display_name,password_hash,role,dept_id,quota_bytes) VALUES(?,?,?,?,?,?,?)", item.id, item.id, item.id, string(hash), item.role, item.dept, 20<<30); e != nil {
			t.Fatal(e)
		}
		token, csrf := newID(), newID()
		if _, e = db.Exec("INSERT INTO sessions(token_hash,user_id,csrf,expires_at) VALUES(?,?,?,?)", digest(token), item.id, csrf, time.Now().Add(time.Hour)); e != nil {
			t.Fatal(e)
		}
		clients[item.id] = testClient{cookie: &http.Cookie{Name: "gmpan_session", Value: token}, csrf: csrf, user: &User{ID: item.id, Role: item.role, DeptID: item.dept, Enabled: true}}
	}
	return s, clients
}
func call(t *testing.T, s *Server, c testClient, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if b, ok := body.([]byte); ok {
		reader = bytes.NewReader(b)
	} else if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "http://localhost"+path, reader)
	if c.cookie != nil {
		r.AddCookie(c.cookie)
		r.Header.Set("X-CSRF-Token", c.csrf)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
}
func insertEntry(t *testing.T, s *Server, id, scope, owner, dept, name, remark string) {
	t.Helper()
	u := &User{ID: owner, DeptID: dept}
	key, _, _ := space(u, scope)
	_, e := s.DB.Exec("INSERT INTO entries(id,space_key,scope,owner_id,dept_id,kind,name,name_hash,remark,scan_status) VALUES(?,?,?,?,?,'file',?,?,?,'pending')", id, key, scope, owner, dept, name, digest(strings.ToLower(name)), remark)
	if e != nil {
		t.Fatal(e)
	}
}

func TestIntegrationPermissionsAndRemarkSearch(t *testing.T) {
	s, c := testServer(t)
	insertEntry(t, s, "p", "personal", "alice", "", "private.txt", "保密报销")
	insertEntry(t, s, "d", "department", "alice", "sales", "dept.txt", "部门报销")
	insertEntry(t, s, "g", "company", "alice", "", "public.txt", "年会报销 %_")
	expectStatus(t, call(t, s, c["bob"], "GET", "/api/files/p", nil), 404)
	expectStatus(t, call(t, s, c["admin"], "GET", "/api/files/p", nil), 200)
	expectStatus(t, call(t, s, c["admin"], "GET", "/api/files/d", nil), 200)
	expectStatus(t, call(t, s, c["manager"], "GET", "/api/files/d", nil), 200)
	for _, who := range []string{"admin", "alice", "bob"} {
		w := call(t, s, c[who], "GET", "/api/files?scope=company&q=报销", nil)
		expectStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "保密") || strings.Contains(w.Body.String(), "部门报销") || !strings.Contains(w.Body.String(), "年会报销") {
			t.Fatal("search permission or remark match failed")
		}
	}
	w := call(t, s, c["bob"], "GET", "/api/files?scope=company&q=%25_", nil)
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "public.txt") {
		t.Fatal("literal wildcard search failed")
	}
	expectStatus(t, call(t, s, c["alice"], "PATCH", "/api/files/g", map[string]string{"name": "public.txt", "remark": "新的备注"}), 200)
	expectStatus(t, call(t, s, c["bob"], "GET", "/api/files/g", nil), 404)
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/files/g/approve", map[string]bool{"approve": true}), 400)
	noCSRF := c["alice"]
	noCSRF.csrf = ""
	expectStatus(t, call(t, s, noCSRF, "POST", "/api/folders", map[string]string{"scope": "personal", "name": "blocked"}), 403)
	if _, e := s.DB.Exec("UPDATE users SET dept_id='finance' WHERE id='alice'"); e != nil {
		t.Fatal(e)
	}
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/files/d", nil), 404)
	if _, e := s.DB.Exec("UPDATE users SET enabled=0 WHERE id='bob'"); e != nil {
		t.Fatal(e)
	}
	expectStatus(t, call(t, s, c["bob"], "GET", "/api/me", nil), 401)
}

func runTestImport(t *testing.T, s *Server) string {
	t.Helper()
	id := newID()
	root, e := canonicalRoot(s.Config.LegacyRoot)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("INSERT INTO import_jobs(id,root_hash,user_id) VALUES(?,?,'admin')", id, sourceHash(root, "")); e != nil {
		t.Fatal(e)
	}
	s.runImport(context.Background(), id, root, "admin")
	var status string
	var errs int
	if e = s.DB.QueryRow("SELECT status,errors FROM import_jobs WHERE id=?", id).Scan(&status, &errs); e != nil {
		t.Fatal(e)
	}
	if status != "completed" || errs != 0 {
		t.Fatalf("import status=%s errors=%d", status, errs)
	}
	return id
}
func TestIntegrationImportIdempotencyAndSourceProtection(t *testing.T) {
	s, _ := testServer(t)
	dir := filepath.Join(s.Config.LegacyRoot, "规章制度")
	os.MkdirAll(dir, 0700)
	source := filepath.Join(dir, "员工手册.txt")
	original := []byte("员工手册原文")
	if e := os.WriteFile(source, original, 0600); e != nil {
		t.Fatal(e)
	}
	before, _ := os.Stat(source)
	runTestImport(t, s)
	var id, parent string
	if e := s.DB.QueryRow("SELECT id,parent_id FROM entries WHERE name='员工手册.txt'").Scan(&id, &parent); e != nil {
		t.Fatal(e)
	}
	if parent == "" {
		t.Fatal("directory structure lost")
	}
	if _, e := s.DB.Exec("UPDATE entries SET name='展示名.txt',name_hash=?,remark='可搜索的备注' WHERE id=?", digest("展示名.txt"), id); e != nil {
		t.Fatal(e)
	}
	runTestImport(t, s)
	var count int
	if e := s.DB.QueryRow("SELECT COUNT(*) FROM entries").Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 3 {
		t.Fatalf("duplicated entries=%d", count)
	}
	var name, remark, scope, publication string
	if e := s.DB.QueryRow("SELECT name,remark,scope,publication FROM entries WHERE id=?", id).Scan(&name, &remark, &scope, &publication); e != nil {
		t.Fatal(e)
	}
	if name != "展示名.txt" || remark != "可搜索的备注" || scope != "company" || publication != "published" {
		t.Fatal("import overwrote metadata or company visibility")
	}
	after, _ := os.Stat(source)
	b, _ := os.ReadFile(source)
	if !bytes.Equal(b, original) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("source file changed during indexing")
	}
	os.Remove(source)
	runTestImport(t, s)
	var missing bool
	s.DB.QueryRow("SELECT source_missing FROM entries WHERE id=?", id).Scan(&missing)
	if !missing {
		t.Fatal("missing source not marked")
	}
	os.WriteFile(source, []byte("更新后的资料"), 0600)
	runTestImport(t, s)
	var scan string
	s.DB.QueryRow("SELECT scan_status,source_missing FROM entries WHERE id=?", id).Scan(&scan, &missing)
	if scan != "pending" || missing {
		t.Fatal("reappeared file not rechecked")
	}
	s.DB.QueryRow("SELECT remark FROM entries WHERE id=?", id).Scan(&remark)
	if remark != "可搜索的备注" {
		t.Fatal("remark overwritten")
	}
}

type cleanTestScanner struct{}

func (cleanTestScanner) Check(context.Context, string) (string, string) { return "clean", "" }
func TestIntegrationChunkResumeScanAndRange(t *testing.T) {
	s, c := testServer(t)
	data := bytes.Repeat([]byte("a"), int(ChunkSize)+23)
	w := call(t, s, c["alice"], "POST", "/api/uploads", map[string]any{"scope": "personal", "parent_id": "", "name": "续传.txt", "remark": "上传备注关键词", "size": len(data)})
	expectStatus(t, w, 201)
	var init struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &init)
	expectStatus(t, call(t, s, c["bob"], "GET", "/api/uploads/"+init.ID, nil), 404)
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/uploads/"+init.ID+"/chunks/0", []byte("short")), 400)
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/uploads/"+init.ID+"/chunks/0", data[:ChunkSize]), 200)
	status := call(t, s, c["alice"], "GET", "/api/uploads/"+init.ID, nil)
	expectStatus(t, status, 200)
	if !strings.Contains(status.Body.String(), digest(string(data[:ChunkSize]))) {
		t.Fatal("resume fingerprint absent")
	}
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/uploads/"+init.ID+"/complete", nil), 409)
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/uploads/"+init.ID+"/chunks/1", data[ChunkSize:]), 200)
	complete := call(t, s, c["alice"], "POST", "/api/uploads/"+init.ID+"/complete", nil)
	expectStatus(t, complete, 201)
	var item struct {
		ID string `json:"id"`
	}
	json.Unmarshal(complete.Body.Bytes(), &item)
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/files/"+item.ID+"/content", nil), 423)
	expectStatus(t, call(t, s, c["admin"], "GET", "/api/files/"+item.ID+"/content", nil), 423)
	s.scan = cleanTestScanner{}
	s.scanOne(context.Background(), item.ID)
	r := httptest.NewRequest("GET", "http://localhost/api/files/"+item.ID+"/content", nil)
	r.AddCookie(c["alice"].cookie)
	r.Header.Set("Range", "bytes=4-12")
	rangeResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(rangeResponse, r)
	expectStatus(t, rangeResponse, 206)
	if !bytes.Equal(rangeResponse.Body.Bytes(), data[4:13]) {
		t.Fatal("range data mismatch")
	}
	again := call(t, s, c["alice"], "POST", "/api/uploads/"+init.ID+"/complete", nil)
	expectStatus(t, again, 200)
	if !strings.Contains(again.Body.String(), item.ID) {
		t.Fatal("completion not idempotent")
	}
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/files/"+item.ID+"/trash", nil), 200)
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/files/"+item.ID+"/content", nil), 404)
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/files/"+item.ID+"/restore", nil), 200)
	v, e := s.getEntry(item.ID, c["alice"].user)
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.entryPath(v)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(p, []byte("changed"), 0600)
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/files/"+item.ID+"/content", nil), 423)
}
func TestIntegrationPhoneLoginQuotaAndUsernameUniqueness(t *testing.T) {
	s, c := testServer(t)
	if _, e := s.DB.Exec("UPDATE users SET mobile='13800000000',quota_bytes=100 WHERE id='alice'"); e != nil {
		t.Fatal(e)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "13800000000", "password": "testPassword2026"}), 200)
	for i, n := range []int{80, 40} {
		w := call(t, s, c["alice"], "POST", "/api/uploads", map[string]any{"scope": "personal", "name": fmt.Sprintf("quota%d.txt", i), "size": n})
		want := 201
		if i == 1 {
			want = 400
		}
		expectStatus(t, w, want)
	}
	expectStatus(t, call(t, s, c["bob"], "POST", "/api/admin/users", map[string]string{}), 403)
}
