//go:build windows

package setup

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"gmpan/internal/app"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWizardLocalBoundary(t *testing.T) {
	w := New(t.TempDir())
	w.Host = "127.0.0.1:18282"
	for _, item := range []struct {
		remote, host, token, origin string
		status                      int
	}{
		{"127.0.0.1:50001", w.Host, w.Token, "", 200},
		{"192.168.1.8:50001", w.Host, w.Token, "", 403},
		{"127.0.0.1:50001", "attacker.example", w.Token, "", 403},
		{"127.0.0.1:50001", w.Host, "", "", 403},
		{"127.0.0.1:50001", w.Host, w.Token, "http://attacker.example", 403},
	} {
		r := httptest.NewRequest("GET", "http://"+item.host+"/api/setup/info", nil)
		r.RemoteAddr = item.remote
		r.Header.Set("X-Setup-Token", item.token)
		r.Header.Set("Origin", item.origin)
		out := httptest.NewRecorder()
		w.Handler().ServeHTTP(out, r)
		if out.Code != item.status {
			t.Fatalf("boundary status=%d want=%d", out.Code, item.status)
		}
		if strings.Contains(out.Body.String(), w.Token) {
			t.Fatal("setup token leaked in response")
		}
	}
}

func TestInstallerPathProtection(t *testing.T) {
	for _, path := range []string{`C:\`, `relative`, `\\server\share\data`, "C:\\bad\npath"} {
		if localPath(path) == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	if !overlaps(`C:\GmPan`, `c:\gmpan\data`) || overlaps(`C:\GmPan`, `C:\GmPanFiles`) {
		t.Fatal("directory overlap comparison failed")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "escape.zip")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	entry, _ := z.Create("../outside.txt")
	_, _ = entry.Write([]byte("blocked"))
	z.Close()
	f.Close()
	if extract(source, filepath.Join(parent, "target")) == nil {
		t.Fatal("ZIP traversal accepted")
	}
	if _, err = os.Stat(filepath.Join(parent, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("ZIP escaped installation directory")
	}
}

func TestInstallerRejectsUnknownFields(t *testing.T) {
	w := New(t.TempDir())
	w.Host = "127.0.0.1:18282"
	r := httptest.NewRequest("POST", "http://"+w.Host+"/api/setup/check", bytes.NewBufferString(`{"sms":{"app_secret":"must-not-be-used"}}`))
	r.RemoteAddr = "127.0.0.1:50001"
	r.Header.Set("X-Setup-Token", w.Token)
	out := httptest.NewRecorder()
	w.Handler().ServeHTTP(out, r)
	if out.Code != 400 || strings.Contains(out.Body.String(), "must-not-be-used") {
		t.Fatal("unknown configuration accepted or echoed")
	}
}

func TestInstallerWebsitePortRange(t *testing.T) {
	if !elevated() {
		t.Skip("installer validation requires a local Windows administrator")
	}
	t.Setenv("GMPAN_DSN", "")
	base := t.TempDir()
	w := New(filepath.Join(base, "bundle"))
	p := Request{Directory: filepath.Join(base, "site"), Storage: filepath.Join(base, "files"), Company: "端口校验测试", Host: "127.0.0.1", DBMode: "new", DBPort: 13306, Admin: "admin", Password: "PortRangeTest2026!"}
	for _, port := range []int{1, 80, 443, 1023, 1024, 65535} {
		p.Port = port
		// Subsequent checks may reject an occupied port, existing service or
		// missing payload. None may reject a valid website port by its range.
		if err := w.validate(p); err != nil && strings.Contains(err.Error(), "端口需为") {
			t.Fatalf("valid website port %d rejected: %v", port, err)
		}
	}
	for _, port := range []int{0, -1, 65536} {
		p.Port = port
		if err := w.validate(p); err == nil || err.Error() != "网站端口需为 1～65535" {
			t.Fatalf("invalid website port %d accepted: %v", port, err)
		}
	}
	p.Port, p.DBPort = 80, 1023
	if err := w.validate(p); err == nil || err.Error() != "数据库端口需为 1024～65535" {
		t.Fatalf("database port range changed unintentionally: %v", err)
	}
}

func TestInstallerPasswordPolicy(t *testing.T) {
	if !elevated() {
		t.Skip("installer validation requires a local Windows administrator")
	}
	t.Setenv("GMPAN_DSN", "")
	base := t.TempDir()
	w := New(filepath.Join(base, "bundle"))
	p := Request{Directory: filepath.Join(base, "site"), Storage: filepath.Join(base, "files"), Company: "密码规则测试", Host: "127.0.0.1", Port: freePort(18589), DBMode: "new", DBPort: freePort(23589), Admin: "admin"}
	for _, password := range []string{"abc1234", "abcdefgh", "12345678"} {
		p.Password = password
		if err := w.validate(p); err == nil || err.Error() != "超管"+app.PasswordRequirement {
			t.Fatal("installer accepted an invalid password or used an inconsistent rule")
		}
	}
	p.Password = "abcd1234"
	if err := w.validate(p); err != nil && strings.Contains(err.Error(), "密码") {
		t.Fatal("installer rejected an eight-character alphanumeric password")
	}
}

func TestInstallationDefaultURLs(t *testing.T) {
	for _, item := range []struct{ listen, cert, want string }{
		{"192.168.2.92:80", "", "http://192.168.2.92"},
		{"192.168.2.92:443", "server.crt", "https://192.168.2.92"},
		{"192.168.2.92:80", "server.crt", "https://192.168.2.92:80"},
		{"192.168.2.92:443", "", "http://192.168.2.92:443"},
		{"192.168.2.92:8080", "", "http://192.168.2.92:8080"},
		{"192.168.2.92:8443", "server.crt", "https://192.168.2.92:8443"},
		{"127.0.0.1:18080", "", "http://127.0.0.1:18080"},
	} {
		if got := installationURL(app.Config{Listen: item.listen, TLSCert: item.cert}); got != item.want {
			t.Fatalf("URL %s != %s", got, item.want)
		}
	}
}

func TestInstallerFilePermissionsRemainReadable(t *testing.T) {
	if !elevated() {
		t.Skip("requires administrator ACL configuration")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("test-only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := protect(path); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "test-only" {
		t.Fatal("protected file is not readable by administrators")
	}
}

// This opt-in test installs the real isolated services and leaves the files for
// inspection. Services are stopped/deleted after verification, never on other DBs.
func TestIntegrationWindowsInstallation(t *testing.T) {
	bundle := os.Getenv("GMPAN_SETUP_TEST_BUNDLE")
	base := os.Getenv("GMPAN_SETUP_TEST_DIRECTORY")
	if bundle == "" || base == "" {
		t.Skip("set explicit local test bundle and an empty test directory")
	}
	base, err := filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(base), string(filepath.Separator)+".local"+string(filepath.Separator)+"setup-test-") {
		t.Fatal("test directory must be within .local/setup-test-*")
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("test root already exists")
	}
	if err = serviceAbsent(appService); err != nil {
		t.Fatal(err)
	}
	if err = serviceAbsent(mysqlService); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, name := range []string{appService, mysqlService} {
			// Only clean up services whose binary path belongs to this test directory.
			if err := removeTestService(name, base); err != nil {
				t.Error(err)
			}
		}
	}()
	w := New(bundle)
	p := Request{Directory: filepath.Join(base, "site"), Storage: filepath.Join(base, "files"), Company: "GmPan 部署测试", Host: "127.0.0.1", Port: freePort(18380), DBMode: "new", DBPort: freePort(23406), Admin: "setup_admin", Password: randomSecret()}
	p.PlainHTTP = os.Getenv("GMPAN_SETUP_TEST_PLAIN_HTTP") == "1"
	if os.Getenv("GMPAN_SETUP_TEST_LAN") == "1" {
		addresses, _ := net.InterfaceAddrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() {
				p.Host = ip.String()
				break
			}
		}
		if p.Host == "127.0.0.1" {
			t.Fatal("no private IPv4 for LAN installation test")
		}
		p.Firewall = true
		defer firewall(p.Directory, p.Port, false)
	}
	w.install(p)
	if !w.state.Done || w.state.Error != "" {
		t.Fatalf("installation failed: %s; log: %s", w.state.Error, w.state.Log)
	}
	if err = verifyTestService(appService, true); err != nil {
		t.Fatal(err)
	}
	if err = verifyTestService(mysqlService, false); err != nil {
		t.Fatal(err)
	}
	c, err := os.ReadFile(filepath.Join(p.Directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(c, []byte(`"sms"`)) {
		t.Fatal("SMS found in generated configuration")
	}
	if _, err := os.Stat(filepath.Join(p.Directory, "mysql-bootstrap.sql")); !os.IsNotExist(err) {
		t.Fatal("bootstrap SQL credentials retained")
	}
	var cfg struct {
		DSN string `json:"mysql_dsn"`
	}
	if err = json.Unmarshal(c, &cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.LoadConfig(filepath.Join(p.Directory, "config.json"))
	if err != nil || (p.Host != "127.0.0.1" && p.PlainHTTP && (!loaded.AllowLANHTTP || loaded.TLSCert != "")) {
		t.Fatal("installed HTTP preference was not retained")
	}
	db, err := app.OpenDB(app.Config{DSN: cfg.DSN})
	if err != nil {
		t.Fatal("runtime database account cannot connect")
	}
	var version string
	if err = db.QueryRow("SELECT VERSION()").Scan(&version); err != nil || !strings.HasPrefix(version, "8.4.") {
		db.Close()
		t.Fatal("installed server is not MySQL 8.4 LTS")
	}
	rows, err := db.Query("SHOW GRANTS")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	for rows.Next() {
		var grant string
		if err = rows.Scan(&grant); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(grant, "ALL PRIVILEGES") || strings.Contains(grant, "CREATE,") {
			t.Fatal("runtime database user retains schema/admin privileges")
		}
	}
	rows.Close()
	db.Close()
	certPath := ""
	if p.Host != "127.0.0.1" {
		if !p.PlainHTTP {
			certPath = filepath.Join(p.Directory, "certs", "server.crt")
		}
		if err = runCommand(10*time.Second, "netsh.exe", "advfirewall", "firewall", "show", "rule", "name="+firewallName(p.Directory)); err != nil {
			t.Fatal("LAN firewall rule was not created")
		}
	}
	checkInstalledSite(t, w.state.URL, p.Admin, p.Password, certPath)
	w.state.URL = checkInstalledSiteRestart(t, w.state.URL, p.Admin, p.Password, certPath)
	// Restart both real services; service settings alone do not prove they start.
	if err = restartTestServices(); err != nil {
		t.Fatal(err)
	}
	if err = health(w.state.URL, certPath); err != nil {
		t.Fatal(err)
	}
	checkInstalledSite(t, w.state.URL, p.Admin, p.Password, certPath)
	for _, name := range []string{"logs/setup.log", "安装结果.txt"} {
		b, err := os.ReadFile(filepath.Join(p.Directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(p.Password)) || bytes.Contains(b, []byte(cfg.DSN)) {
			t.Fatal("credential leaked to log/result")
		}
	}
	if err := w.validate(p); err == nil {
		t.Fatal("installed target accepted for overwrite")
	}
	t.Log("real MySQL 8.4 + app services, low-privilege accounts, startup configuration, login, upload, scan, download, service restart and overwrite guard verified")
	checkInstalledUninstall(t, bundle, p)
}

func checkInstalledSite(t *testing.T, url, user, password, certPath string) {
	t.Helper()
	transport := &http.Transport{}
	if certPath != "" {
		b, err := os.ReadFile(certPath)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			t.Fatal("invalid TLS certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	request := func(method, path string, body io.Reader, cookie *http.Cookie, csrf string) []byte {
		r, err := http.NewRequest(method, url+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("%s %s status %d: %s", method, path, res.StatusCode, b)
		}
		return b
	}
	b, _ := json.Marshal(map[string]string{"account": user, "password": password})
	res, err := client.Post(url+"/api/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	login, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || len(res.Cookies()) == 0 {
		t.Fatalf("login status=%d", res.StatusCode)
	}
	if res.Cookies()[0].Secure != (certPath != "") {
		t.Fatal("session cookie does not match the selected HTTP/HTTPS protocol")
	}
	var u struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(login, &u)
	cookie := res.Cookies()[0]
	public := request("GET", "/api/public-config", nil, nil, "")
	var pc struct {
		SMS bool `json:"sms_enabled"`
	}
	json.Unmarshal(public, &pc)
	if pc.SMS {
		t.Fatal("SMS unexpectedly enabled")
	}
	verifyUpload(t, request, cookie, u.CSRF)
}
