package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, s *Server, c testClient) SiteSettings {
	t.Helper()
	w := call(t, s, c, "GET", "/api/admin/site-settings", nil)
	expectStatus(t, w, 200)
	var reply struct {
		Settings SiteSettings `json:"settings"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &reply); e != nil {
		t.Fatal(e)
	}
	return reply.Settings
}
func TestIntegrationSiteSettingsAndLogo(t *testing.T) {
	s, c := testServer(t)
	for _, endpoint := range []string{"site-settings", "sms-settings"} {
		expectStatus(t, call(t, s, c["alice"], "GET", "/api/admin/"+endpoint, nil), 403)
	}
	expectStatus(t, call(t, s, testClient{}, "GET", "/api/admin/site-settings", nil), 401)
	v := readSettings(t, s, c["admin"])
	v.CompanyName = "演示公司全称"
	v.CompanyShortName = "演示公司"
	v.DefaultTheme = "library"
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/admin/site-settings", v), 403)
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", v), 200)
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", v), 409)
	public := call(t, s, testClient{}, "GET", "/api/public-config", nil)
	expectStatus(t, public, 200)
	if !strings.Contains(public.Body.String(), "演示公司全称") || strings.Contains(public.Body.String(), s.Config.DataDir) || strings.Contains(public.Body.String(), "legacy_root") {
		t.Fatal("public config missing branding or exposes paths")
	}
	v = readSettings(t, s, c["admin"])
	v.CopyrightURL = "javascript:alert(1)"
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", v), 400)
	v = readSettings(t, s, c["admin"])
	v.DataDir = s.Config.LogDir
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", v), 400)
	v = readSettings(t, s, c["admin"])
	v.LegacyRoot = s.Config.DataDir
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", v), 400)
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-logo", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`)), 400)
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/admin/site-logo", imageBytes.Bytes()), 403)
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-logo", imageBytes.Bytes()), 200)
	logo := call(t, s, testClient{}, "GET", "/api/site-logo", nil)
	expectStatus(t, logo, 200)
	if logo.Header().Get("Content-Type") != "image/png" {
		t.Fatal("unsafe logo type")
	}
	restarted, e := NewServer(s.DB, s.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	if restarted.site.CompanyName != "演示公司全称" || restarted.site.DefaultTheme != "library" || len(restarted.siteLogo) == 0 {
		t.Fatal("settings/logo did not survive restart")
	}
	expectStatus(t, call(t, restarted, c["admin"], "DELETE", "/api/admin/site-logo", nil), 200)
	expectStatus(t, call(t, restarted, testClient{}, "GET", "/api/site-logo", nil), 404)
}
func TestIntegrationPendingDirectoryConfiguration(t *testing.T) {
	s, c := testServer(t)
	v := readSettings(t, s, c["admin"])
	v.StorageRoot = t.TempDir()
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", v), 200)
	if sameDirectory(s.Config.StorageRoot, v.StorageRoot) {
		t.Fatal("paths changed during active process")
	}
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/uploads", map[string]any{"scope": "personal", "name": "a.txt", "size": 1}), 409)
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/admin/imports", nil), 409)
	restarted, e := NewServer(s.DB, s.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	if !sameDirectory(restarted.Config.StorageRoot, v.StorageRoot) {
		t.Fatal("pending paths not applied after restart")
	}
	insertEntry(t, restarted, "local-file", "personal", "alice", "", "keep.txt", "")
	s.DB.Exec("UPDATE entries SET storage_kind='local' WHERE id='local-file'")
	v = readSettings(t, restarted, c["admin"])
	v.StorageRoot = t.TempDir()
	expectStatus(t, call(t, restarted, c["admin"], "PUT", "/api/admin/site-settings", v), 409)
	insertEntry(t, restarted, "legacy-file", "company", "admin", "", "original.txt", "")
	s.DB.Exec("UPDATE entries SET storage_kind='legacy' WHERE id='legacy-file'")
	v = readSettings(t, restarted, c["admin"])
	v.StorageRoot = t.TempDir()
	expectStatus(t, call(t, restarted, c["admin"], "PUT", "/api/admin/site-settings", v), 409)
	if _, e := os.Stat(filepath.Join(restarted.Config.DataDir, "objects")); e != nil {
		t.Fatal(e)
	}
}
func TestIntegrationSMSAdministration(t *testing.T) {
	s, c := testServer(t)
	sent := 0
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Gm-AppKey") != "test-key" || r.Header.Get("X-Gm-Sign") != SMSSign("test-key", "test-secret", r.Header.Get("X-Gm-Timestamp"), r.Header.Get("X-Gm-Nonce"), body) {
			t.Error("configured credentials not used")
		}
		if !strings.Contains(string(body), `"scene":"staff.login"`) {
			t.Error("configured scene not used")
		}
		io.WriteString(w, `{"code":0}`)
	}))
	defer provider.Close()
	s.smsClient = provider.Client()
	var config map[string]any
	w := call(t, s, c["admin"], "GET", "/api/admin/sms-settings", nil)
	expectStatus(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &config)
	delete(config, "has_app_key")
	delete(config, "has_app_secret")
	config["enabled"] = true
	config["base_url"] = provider.URL
	config["login_scene"] = "staff.login"
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/sms-settings", config), 400)
	config["app_key"] = "test-key"
	config["app_secret"] = "test-secret"
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/admin/sms-settings", config), 403)
	w = call(t, s, c["admin"], "PUT", "/api/admin/sms-settings", config)
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "test-secret") || strings.Contains(w.Body.String(), "test-key") {
		t.Fatal("credentials echoed")
	}
	config["revision"] = float64(s.smsSettings.Revision)
	config["app_key"] = ""
	config["app_secret"] = ""
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/sms-settings", config), 200)
	if s.smsSettings.AppSecret != "test-secret" {
		t.Fatal("blank overwrote secret")
	}
	s.DB.Exec("UPDATE users SET mobile='13800000000' WHERE id='alice'")
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}), 200)
	if sent != 1 {
		t.Fatalf("expected one mock SMS request, got %d", sent)
	}
	public := call(t, s, testClient{}, "GET", "/api/public-config", nil)
	if strings.Contains(public.Body.String(), "app_secret") || strings.Contains(public.Body.String(), provider.URL) {
		t.Fatal("public SMS settings leakage")
	}
	restarted, e := NewServer(s.DB, s.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	if !restarted.smsEnabled() || restarted.smsSettings.AppSecret != "test-secret" {
		t.Fatal("SMS config not durable")
	}
	config["revision"] = float64(restarted.smsSettings.Revision)
	config["enabled"] = false
	config["clear_credentials"] = true
	w = call(t, restarted, c["admin"], "PUT", "/api/admin/sms-settings", config)
	expectStatus(t, w, 200)
	expectStatus(t, call(t, restarted, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}), 503)
	if restarted.smsSettings.AppSecret != "" || sent != 1 {
		t.Fatal("credentials not cleared or SMS sent while disabled")
	}
}
func TestIntegrationCompanyDirectoryBrowsing(t *testing.T) {
	s, c := testServer(t)
	for _, f := range []struct{ id, parent, name string }{{"f", "", "视频资料"}, {"child", "f", "产品培训"}, {"hidden", "", "草稿目录"}} {
		insertEntry(t, s, f.id, "company", "admin", "", f.name, "")
		if _, e := s.DB.Exec("UPDATE entries SET kind='folder',parent_id=? WHERE id=?", f.parent, f.id); e != nil {
			t.Fatal(e)
		}
	}
	s.DB.Exec("UPDATE entries SET publication='pending' WHERE id='hidden'")
	for _, f := range []struct{ id, name, mime string }{{"v", "培训.mp4", "video/mp4"}, {"z", "课程.ZIP", "application/zip"}, {"d", "说明.txt", "text/plain"}, {"edge", "notzip", "text/plain"}} {
		insertEntry(t, s, f.id, "company", "admin", "", f.name, "仅备注命中")
		s.DB.Exec("UPDATE entries SET parent_id='child',mime=? WHERE id=?", f.mime, f.id)
	}
	insertEntry(t, s, "private-dir", "personal", "alice", "", "个人目录", "")
	s.DB.Exec("UPDATE entries SET kind='folder' WHERE id='private-dir'")
	root := call(t, s, c["alice"], "GET", "/api/company/folders", nil)
	expectStatus(t, root, 200)
	if strings.Contains(root.Body.String(), "草稿目录") || !strings.Contains(root.Body.String(), "视频资料") {
		t.Fatal("directory publication rules broken")
	}
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/company/folders?parent=private-dir", nil), 400)
	list := func(query string) []Entry {
		w := call(t, s, c["alice"], "GET", "/api/files?scope=company&directory=true&parent=f&"+query, nil)
		expectStatus(t, w, 200)
		var reply struct {
			Items []Entry `json:"items"`
		}
		json.Unmarshal(w.Body.Bytes(), &reply)
		return reply.Items
	}
	if items := list("q=仅备注命中"); len(items) != 0 {
		t.Fatal("current directory search escaped into descendants")
	}
	items := list("recursive=true&q=仅备注命中&type=archive")
	if len(items) != 1 || items[0].ID != "z" {
		t.Fatalf("archive filter mismatch: %+v", items)
	}
	items = list("recursive=true&q=仅备注命中&type=resource")
	if len(items) != 2 {
		t.Fatalf("ordinary file filter mismatch: %+v", items)
	}
	items = list("type=video")
	if len(items) != 1 || items[0].ID != "child" {
		t.Fatal("directory disappeared under video filter")
	}
}
