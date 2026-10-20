package app

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationRuntimeControls(t *testing.T) {
	s, c := testServer(t)
	s.Config.Listen = "127.0.0.1:18080"
	for _, user := range []string{"alice", "maint"} {
		expectStatus(t, call(t, s, c[user], "GET", "/api/admin/directories", nil), 403)
		expectStatus(t, call(t, s, c[user], "PUT", "/api/admin/listen-port", map[string]any{"port": 18090}), 403)
		expectStatus(t, call(t, s, c[user], "POST", "/api/admin/restart", map[string]any{}), 403)
	}
	v := readSettings(t, s, c["admin"])
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/admin/restart", map[string]any{"revision": v.Revision}), 409)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/listen-port", map[string]any{"port": port, "revision": v.Revision}), 409)
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/listen-port", map[string]any{"port": 65536, "revision": v.Revision}), 400)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port = free.Addr().(*net.TCPAddr).Port
	free.Close()
	if port == 18080 {
		t.Fatal("test selected current port")
	}
	w := call(t, s, c["admin"], "PUT", "/api/admin/listen-port", map[string]any{"port": port, "revision": v.Revision})
	expectStatus(t, w, 200)
	if s.Config.Listen != "127.0.0.1:18080" || s.site.ListenPort != port {
		t.Fatal("port changed before site restart")
	}
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/listen-port", map[string]any{"port": port, "revision": v.Revision}), 409)
	s.SetRestartHandler(func() {})
	if _, err = s.DB.Exec("INSERT INTO import_jobs(id,root_hash,user_id) VALUES('running-test','test','admin')"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/admin/restart", map[string]any{"revision": s.site.Revision}), 409)
	s.DB.Exec("UPDATE import_jobs SET status='completed' WHERE id='running-test'")
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/admin/restart", map[string]any{"revision": s.site.Revision}), 202)
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/folders", map[string]string{"scope": "company", "name": "blocked"}), 503)
}

func TestIntegrationDirectoryBrowser(t *testing.T) {
	s, c := testServer(t)
	root := t.TempDir()
	for _, name := range []string{"可选择目录", ".gmpan"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "private-file.txt"), []byte("not listed"), 0600); err != nil {
		t.Fatal(err)
	}
	w := call(t, s, c["admin"], "GET", "/api/admin/directories?path="+url.QueryEscape(root), nil)
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "private-file.txt") || strings.Contains(w.Body.String(), ".gmpan") {
		t.Fatal("internal directories or files exposed in directory picker")
	}
	var result struct {
		Items []struct {
			Name string `json:"name"`
		}
		Selectable bool `json:"selectable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Name != "可选择目录" {
		t.Fatal("readable server directory missing")
	}
	expectStatus(t, call(t, s, c["admin"], "GET", "/api/admin/directories?path="+url.QueryEscape(filepath.Join(root, ".gmpan")), nil), 400)
	expectStatus(t, call(t, s, c["admin"], "GET", "/api/admin/directories?path=relative", nil), 400)
	expectStatus(t, call(t, s, c["admin"], "GET", "/api/admin/directories?path="+url.QueryEscape(s.Config.LogDir), nil), 400)
}
