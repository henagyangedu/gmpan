package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gmpan/internal/app"
)

func TestIntegrationSiteRestartAndPortChange(t *testing.T) {
	dsn := os.Getenv("GMPAN_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated MySQL test instance")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Addr != "127.0.0.1:13386" || parsed.DBName != "" {
		t.Fatal("test only permits the isolated local MySQL instance")
	}
	root, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("gmpan_restart_test_%d", time.Now().UnixNano())
	if _, err = root.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	parsed.DBName = name
	db, err := sql.Open("mysql", parsed.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); root.Exec("DROP DATABASE " + name); root.Close() })
	if err = app.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err = app.CreateAdmin(db, "runtime_admin", "RuntimeTestOnly2026!", 20<<30); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	original := filepath.Join(base, "original")
	target := filepath.Join(base, "target")
	logs := filepath.Join(base, "logs")
	for _, path := range []string{original, target, logs} {
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(original, "keep-original.txt"), []byte("preserve source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(target, "new-directory-note.txt"), []byte("new source"), 0600); err != nil {
		t.Fatal(err)
	}
	freePort := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return port
	}
	oldPort, newPort := freePort(), freePort()
	if oldPort == newPort {
		t.Fatal("test port collision")
	}
	cfg := app.Config{Listen: fmt.Sprintf("127.0.0.1:%d", oldPort), DSN: parsed.FormatDSN(), StorageRoot: original, LogDir: logs, MaxFileBytes: 10 << 30, PersonalQuotaBytes: 20 << 30, CompanyName: "GmPan Runtime Test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := app.NewServer(db, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := siteHTTPServer(s)
	originalHandler := server.Handler
	slowStarted := make(chan struct{})
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test-slow-request" {
			close(slowStarted)
			<-r.Context().Done()
			return
		}
		originalHandler.ServeHTTP(w, r)
	})
	go func() { done <- runHTTP(ctx, server, s, s.Config, logger) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(30 * time.Second):
			t.Error("site did not stop")
		}
	})
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second}
	oldURL := fmt.Sprintf("http://127.0.0.1:%d", oldPort)
	newURL := fmt.Sprintf("http://127.0.0.1:%d", newPort)
	await := func(url, previous string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			res, e := client.Get(url + "/api/health")
			if e == nil {
				var v map[string]any
				json.NewDecoder(res.Body).Decode(&v)
				res.Body.Close()
				if res.StatusCode == 200 && v["status"] == "ok" && v["instance_id"] != previous {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("site restart health timeout")
	}
	await(oldURL, "")
	csrf := ""
	request := func(base, method, path string, body any, status int) []byte {
		t.Helper()
		b, _ := json.Marshal(body)
		r, e := http.NewRequest(method, base+path, bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		res, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		reply, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s %s status=%d body=%s", method, path, res.StatusCode, reply)
		}
		return reply
	}
	login := request(oldURL, "POST", "/api/auth/login", map[string]string{"account": "runtime_admin", "password": "RuntimeTestOnly2026!"}, 200)
	var user struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(login, &user)
	csrf = user.CSRF
	var settings struct {
		Settings app.SiteSettings `json:"settings"`
		Active   string           `json:"active_storage_root"`
		Instance string           `json:"instance_id"`
	}
	json.Unmarshal(request(oldURL, "GET", "/api/admin/site-settings", nil, 200), &settings)
	settings.Settings.StorageRoot = target
	json.Unmarshal(request(oldURL, "PUT", "/api/admin/site-settings", settings.Settings, 200), &settings)
	request(oldURL, "POST", "/api/uploads", map[string]any{"scope": "personal", "name": "paused.txt", "size": 1}, 409)
	json.Unmarshal(request(oldURL, "PUT", "/api/admin/listen-port", map[string]any{"port": newPort, "revision": settings.Settings.Revision}, 200), &settings)
	request(oldURL, "GET", "/api/health", nil, 200)
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		client := &http.Client{Timeout: 35 * time.Second}
		if res, err := client.Get(oldURL + "/test-slow-request"); err == nil {
			res.Body.Close()
		}
	}()
	select {
	case <-slowStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("slow connection test did not start")
	}
	request(oldURL, "POST", "/api/admin/restart", map[string]any{"revision": settings.Settings.Revision}, 202)
	await(newURL, settings.Instance)
	select {
	case <-slowDone:
	case <-time.After(time.Second):
		t.Fatal("old active request was not closed during restart")
	}
	request(newURL, "GET", "/api/me", nil, 200)
	json.Unmarshal(request(newURL, "GET", "/api/admin/site-settings", nil, 200), &settings)
	if settings.Active != target || settings.Settings.ListenPort != newPort {
		t.Fatal("pending directory/port not applied")
	}
	if _, err = os.Stat(filepath.Join(original, "keep-original.txt")); err != nil {
		t.Fatal("old source removed")
	}
	before := request(newURL, "GET", "/api/files?scope=company&q=new-directory-note", nil, 200)
	var list struct {
		Items []app.Entry `json:"items"`
	}
	json.Unmarshal(before, &list)
	if len(list.Items) != 0 {
		t.Fatal("new source indexed without explicit scan")
	}
	request(newURL, "POST", "/api/admin/imports", nil, 202)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		json.Unmarshal(request(newURL, "GET", "/api/files?scope=company&q=new-directory-note", nil, 200), &list)
		if len(list.Items) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(list.Items) == 0 {
		t.Fatal("new directory not searchable after scan")
	}
	settings.Settings.StorageRoot = original
	request(newURL, "PUT", "/api/admin/site-settings", settings.Settings, 409)
	if res, e := client.Get(oldURL + "/api/health"); e == nil {
		res.Body.Close()
		t.Fatal("old port still listening after restart")
	}
	t.Log("directory switch, explicit rescan/search, occupied/non-empty guards, new listener, retained login session and graceful restart verified")
}
