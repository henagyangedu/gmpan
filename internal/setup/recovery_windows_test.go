//go:build windows

package setup

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gmpan/internal/app"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

func recoveryPost(t *testing.T, w *Wizard, path string, p any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return setupRequest(w, "POST", "/api/setup/"+path, b)
}

func TestRecoveryReadsSelectedConfigurationOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	write := func(dsn string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"mysql_dsn": dsn, "storage_root": `Z:\unavailable-disk`})
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GMPAN_DSN", "ignored-environment:private@tcp(192.0.2.9:3306)/other_site")
	write("local_user:private@tcp(127.0.0.1:13386)/original_site?parseTime=true")
	c, sum, err := recoveryConfig(root)
	if err != nil || c.DBName != "original_site" || c.Addr != "127.0.0.1:13386" || c.Timeout == 0 || c.ReadTimeout == 0 {
		t.Fatalf("wrong recovery configuration: %v", err)
	}
	write("local_user:different@tcp(127.0.0.1:13386)/original_site")
	_, next, err := recoveryConfig(root)
	if err != nil || sum == next {
		t.Fatal("configuration change not detectable")
	}
	for _, dsn := range []string{"", "u:p@tcp(192.0.2.9:3306)/gmpan", "u:p@tcp(127.0.0.1:3306)/", "u:p@tcp(127.0.0.1:3306)/gmpan?multiStatements=true", "u:p@tcp(127.0.0.1:3306)/gmpan?allowAllFiles=true"} {
		write(dsn)
		if _, _, err := recoveryConfig(root); err == nil || strings.Contains(err.Error(), dsn) && dsn != "" {
			t.Fatal("unsafe config accepted or DSN exposed")
		}
	}
	if _, _, err := recoveryConfig(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing deployment accepted")
	}
}

func TestRecoveryWizardRequiresLocalTokenAndAdmin(t *testing.T) {
	w := New(t.TempDir())
	w.Host = "127.0.0.1:18282"
	for _, path := range []string{"recovery-info", "recovery-reset"} {
		for _, field := range []string{"token", "remote", "origin", "host"} {
			r := httptest.NewRequest("POST", "http://"+w.Host+"/api/setup/"+path, strings.NewReader(`{}`))
			r.RemoteAddr = "127.0.0.1:51000"
			r.Header.Set("X-Setup-Token", w.Token)
			switch field {
			case "token":
				r.Header.Del("X-Setup-Token")
			case "remote":
				r.RemoteAddr = "192.0.2.9:51000"
			case "origin":
				r.Header.Set("Origin", "https://example.invalid")
			case "host":
				r.Host = "example.invalid"
			}
			out := httptest.NewRecorder()
			w.Handler().ServeHTTP(out, r)
			if out.Code != 403 {
				t.Fatal("local wizard authority bypassed")
			}
		}
	}
	if !elevated() {
		out := recoveryPost(t, w, "recovery-info", map[string]string{"directory": t.TempDir()})
		if out.Code != 403 {
			t.Fatal("non-elevated recovery allowed")
		}
		return
	}
	w.checking = true
	if out := recoveryPost(t, w, "recovery-info", map[string]string{}); out.Code != 409 {
		t.Fatal("recovery allowed during another operation")
	}
	if out := setupRequest(w, "POST", "/api/setup/exit", nil); out.Code != 409 {
		t.Fatal("wizard can exit during a pending recovery")
	}
}

func TestRecoveryWizardExistingDatabase(t *testing.T) {
	if !elevated() {
		t.Skip("recovery requires a local Windows administrator")
	}
	dsn := os.Getenv("GMPAN_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated MySQL test instance")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.Addr != "127.0.0.1:13386" || cfg.DBName != "" {
		t.Fatal("recovery tests only allow isolated 127.0.0.1:13386 with an empty database name")
	}
	rootDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "gmpan_recovery_" + randomSecret()[:12]
	if _, err := rootDB.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
		rootDB.Close()
		t.Fatal("cannot create isolated recovery test database")
	}
	t.Cleanup(func() { rootDB.Exec("DROP DATABASE " + name); rootDB.Close() })
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = app.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err = app.CreateAdmin(db, "admin", "OriginalTest2026!", 20<<30); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO users(id,username,display_name,password_hash,role,enabled,quota_bytes) SELECT 'maint','maint','维护员',password_hash,'maintainer',1,quota_bytes FROM users WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	configBytes, _ := json.Marshal(app.Config{DSN: cfg.FormatDSN()})
	if err = os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	// No bundle, payload, program, storage disk or running GmPan service is required.
	w := New(t.TempDir())
	w.Host = "127.0.0.1:18282"
	load := func() string {
		t.Helper()
		out := recoveryPost(t, w, "recovery-info", map[string]string{"directory": directory})
		var result struct {
			Ticket   string                      `json:"ticket"`
			Accounts []struct{ Username string } `json:"accounts"`
		}
		if json.Unmarshal(out.Body.Bytes(), &result) != nil || out.Code != 200 || result.Ticket == "" || len(result.Accounts) != 1 || result.Accounts[0].Username != "admin" {
			t.Fatalf("cannot read existing superadmin: status=%d", out.Code)
		}
		if strings.Contains(out.Body.String(), cfg.Passwd) || strings.Contains(out.Body.String(), "mysql_dsn") {
			t.Fatal("database credentials exposed")
		}
		return result.Ticket
	}
	ticket := load()
	p := map[string]any{"ticket": ticket, "username": "admin", "password": "RecoveredTest2026!", "confirm_password": "Mismatch2026!", "confirmed": true}
	if out := recoveryPost(t, w, "recovery-reset", p); out.Code != 400 {
		t.Fatal("mismatched passwords accepted")
	}
	p["confirm_password"] = p["password"]
	if err = os.WriteFile(configPath, append(configBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if out := recoveryPost(t, w, "recovery-reset", p); out.Code != 409 {
		t.Fatal("changed site config accepted")
	}
	p["ticket"] = load()
	p["username"] = "maint"
	if out := recoveryPost(t, w, "recovery-reset", p); out.Code != 400 {
		t.Fatal("maintainer account reset through superadmin recovery")
	}
	p["username"] = "admin"
	out := recoveryPost(t, w, "recovery-reset", p)
	if out.Code != 200 || strings.Contains(out.Body.String(), p["password"].(string)) {
		t.Fatalf("recovery failed or password exposed: status=%d", out.Code)
	}
	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE username='admin'").Scan(&hash); err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(p["password"].(string))) != nil {
		t.Fatal("wizard did not reset chosen account")
	}
	if out := recoveryPost(t, w, "recovery-reset", p); out.Code != 409 {
		t.Fatal("used recovery confirmation was replayed")
	}
	after, _ := os.ReadFile(configPath)
	if string(after) != string(append(configBytes, '\n')) {
		t.Fatal("recovery changed original configuration")
	}
}
