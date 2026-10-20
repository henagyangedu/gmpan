//go:build windows

package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupRequest(w *Wizard, method, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+w.Host+path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50001"
	r.Header.Set("X-Setup-Token", w.Token)
	out := httptest.NewRecorder()
	w.Handler().ServeHTTP(out, r)
	return out
}

func TestWizardPageDirectoryPicker(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"空部署目录", "已有资料", ".gmpan"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("not listed"), 0600); err != nil {
		t.Fatal(err)
	}
	w := New(t.TempDir())
	w.Host = "127.0.0.1:18282"
	out := setupRequest(w, "GET", "/api/setup/directories?path="+url.QueryEscape(root), nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "空部署目录") || strings.Contains(out.Body.String(), "private.txt") || strings.Contains(out.Body.String(), ".gmpan") {
		t.Fatalf("directory picker response: %d %s", out.Code, out.Body.String())
	}
	var listing struct {
		Path       string `json:"path"`
		Selectable bool   `json:"selectable"`
		Missing    bool   `json:"missing"`
	}
	out = setupRequest(w, "GET", "/api/setup/directories?path="+url.QueryEscape(filepath.Join(root, "尚未创建", "子目录")), nil)
	if err := json.Unmarshal(out.Body.Bytes(), &listing); err != nil || out.Code != 200 || listing.Path != root || !listing.Missing || !listing.Selectable {
		t.Fatalf("missing path fallback: %d %s", out.Code, out.Body.String())
	}
	for _, path := range []string{"relative", `\\server\share`, filepath.Join(root, "private.txt")} {
		out = setupRequest(w, "GET", "/api/setup/directories?path="+url.QueryEscape(path), nil)
		if out.Code != 400 {
			t.Fatalf("invalid path accepted: %q: %s", path, out.Body.String())
		}
	}
	out = setupRequest(w, "GET", "/api/setup/directories", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"selectable":false`) {
		t.Fatalf("disk navigation: %d %s", out.Code, out.Body.String())
	}
	w.state.Running = true
	if out = setupRequest(w, "GET", "/api/setup/directories", nil); out.Code != 409 {
		t.Fatal("directory selection allowed during installation")
	}
}

func TestWizardCheckTimeoutLeavesPageResponsive(t *testing.T) {
	if !elevated() {
		t.Skip("requires read access to Windows service manager as administrator")
	}
	if err := serviceAbsent(appService); err != nil {
		t.Skip("existing GmPan service; leave it untouched")
	}
	// A TCP endpoint that accepts connections but never sends the MySQL greeting
	// reproduces a stalled dependency without modifying any database or service.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "payload"), 0700); err != nil {
		t.Fatal(err)
	}
	m := Manifest{MySQLRoot: "mysql-8.4-test", MySQLVersion: "8.4.test", Files: map[string]string{}}
	for _, name := range []string{"gmpan.exe", "payload/mysql.zip", "payload/clamav.zip", "payload/vc_redist.x64.exe"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte("test-only"), 0600); err != nil {
			t.Fatal(err)
		}
		m.Files[name] = strings.Repeat("0", 64)
	}
	manifest, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(bundle, "payload", "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	w := New(bundle)
	w.Host = "127.0.0.1:18282"
	p := Request{Directory: filepath.Join(t.TempDir(), "site"), Storage: filepath.Join(t.TempDir(), "files"), Company: "GmPan 检查测试", Host: "127.0.0.1", Port: freePort(18580), DBMode: "existing", DBHost: "127.0.0.1", DBPort: listener.Addr().(*net.TCPAddr).Port, DBName: "gmpan", DBUser: "test_only", DBPassword: "not-a-real-password", Admin: "setup_admin", Password: "TestOnlyCheck2026!"}
	body, _ := json.Marshal(p)
	r := httptest.NewRequest("POST", "http://"+w.Host+"/api/setup/check", bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50001"
	r.Header.Set("X-Setup-Token", w.Token)
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	out := httptest.NewRecorder()
	start := time.Now()
	w.Handler().ServeHTTP(out, r.WithContext(ctx))
	if out.Code != 504 || time.Since(start) > 2*time.Second {
		t.Fatalf("stalled check did not time out: %d %s", out.Code, out.Body.String())
	}
	if next := setupRequest(w, "GET", "/api/setup/progress", nil); next.Code != 200 {
		t.Fatal("page status blocked by a slow check")
	}
	if next := setupRequest(w, "POST", "/api/setup/install", body); next.Code != 409 {
		t.Fatal("concurrent installation allowed while check is still running")
	}
	select {
	case conn := <-accepted:
		conn.Close()
	case <-time.After(time.Second):
		t.Fatal("validation never reached test database")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		busy := w.checking
		w.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("finished validation did not release the check slot")
}
