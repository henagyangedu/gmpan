package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestRecoveryPasswordAndExistingData(t *testing.T) {
	s, clients := testServer(t)
	ctx := context.Background()
	newPassword := "RecoveryTest2026!"
	insertEntry(t, s, "retained", "personal", "alice", "", "保留资料.txt", "密码重置不能修改文件")
	path := filepath.Join(s.Config.DataDir, "recovery-retained.txt")
	if err := os.WriteFile(path, []byte("unchanged resource"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec("UPDATE users SET mobile='13800000000' WHERE id='admin'")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		s.attempt("admin", "test-ip", "password")
		s.attempt("13800000000", "test-ip", "password")
	}
	s.attempt("alice", "other-ip", "password")
	s.attempt("admin", "test-ip", "sms")
	for _, username := range []string{"missing", "maint", "alice"} {
		if err := ResetAdminPassword(ctx, s.DB, username, newPassword); !errors.Is(err, ErrRecoveryAccount) {
			t.Fatalf("non-admin account %q accepted: %v", username, err)
		}
	}
	for _, password := range []string{"short", strings.Repeat("x", 73), "Contains\nNewline2026"} {
		if err := ResetAdminPassword(ctx, s.DB, "admin", password); !errors.Is(err, ErrRecoveryPassword) {
			t.Fatal("invalid recovery password accepted")
		}
	}
	if _, err := s.DB.Exec("UPDATE users SET enabled=0 WHERE id='admin'"); err != nil {
		t.Fatal(err)
	}
	if err := ResetAdminPassword(ctx, s.DB, "admin", newPassword); !errors.Is(err, ErrRecoveryAccount) {
		t.Fatal("disabled superadmin was re-enabled")
	}
	if _, err := s.DB.Exec("UPDATE users SET enabled=1 WHERE id='admin'"); err != nil {
		t.Fatal(err)
	}
	if err := ResetAdminPassword(ctx, s.DB, "admin", newPassword); err != nil {
		t.Fatal(err)
	}
	var hash, role, dept string
	var enabled bool
	var quota int64
	if err := s.DB.QueryRow("SELECT password_hash,role,dept_id,enabled,quota_bytes FROM users WHERE id='admin'").Scan(&hash, &role, &dept, &enabled, &quota); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(newPassword)) != nil || role != "admin" || dept != "finance" || !enabled || quota != 20<<30 {
		t.Fatal("password hash or original account attributes incorrect")
	}
	for _, item := range []struct {
		query string
		want  int
	}{
		{"SELECT COUNT(*) FROM sessions WHERE user_id='admin'", 0},
		{"SELECT COUNT(*) FROM sessions WHERE user_id<>'admin'", 4},
		{"SELECT COUNT(*) FROM users", 5},
		{"SELECT COUNT(*) FROM entries WHERE id='retained' AND remark='密码重置不能修改文件'", 1},
		{"SELECT COUNT(*) FROM auth_attempts", 2},
		{"SELECT COUNT(*) FROM audit_logs WHERE action='local_admin_password_reset' AND target_id='admin' AND user_id='' AND result='ok'", 1},
	} {
		var n int
		if err := s.DB.QueryRow(item.query).Scan(&n); err != nil || n != item.want {
			t.Fatalf("unexpected recovery result: %s count=%d err=%v", item.query, n, err)
		}
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "unchanged resource" {
		t.Fatal("resource file changed")
	}
	// Check actual site login and authentication, not just the stored hash.
	expectStatus(t, call(t, s, clients["admin"], "GET", "/api/me", nil), 401)
	expectStatus(t, call(t, s, clients["alice"], "GET", "/api/me", nil), 200)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "admin", "password": "testPassword2026"}), 401)
	login := call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "admin", "password": newPassword})
	expectStatus(t, login, 200)
	if len(login.Result().Cookies()) != 1 {
		t.Fatal("new password did not produce a login cookie")
	}
	expectStatus(t, call(t, s, testClient{cookie: login.Result().Cookies()[0]}, "GET", "/api/me", nil), 200)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "alice", "password": "testPassword2026"}), 200)
}

func TestRecoveryTransactionRollback(t *testing.T) {
	s, _ := testServer(t)
	var before string
	if err := s.DB.QueryRow("SELECT password_hash FROM users WHERE id='admin'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Force the final audit write to fail; preceding password/session writes must roll back.
	if _, err := s.DB.Exec("RENAME TABLE audit_logs TO unavailable_audit_logs"); err != nil {
		t.Fatal(err)
	}
	if err := ResetAdminPassword(context.Background(), s.DB, "admin", "RollbackTest2026!"); err == nil {
		t.Fatal("recovery reported success despite audit failure")
	}
	var after string
	var sessions int
	if err := s.DB.QueryRow("SELECT password_hash FROM users WHERE id='admin'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id='admin'").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if before != after || sessions != 1 {
		t.Fatal("failed recovery partially committed")
	}
}
