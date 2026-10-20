//go:build windows

package setup

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func removeTestService(name, base string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return nil
	}
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(c.BinaryPathName), strings.ToLower(base)) {
		return fmt.Errorf("refuse to remove service outside test root: %s", name)
	}
	stopService(s)
	state, err := s.Query()
	if err != nil {
		return err
	}
	if state.State != svc.Stopped {
		return fmt.Errorf("test service did not stop: %s", name)
	}
	return s.Delete()
}

func verifyTestService(name string, delayed bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	if c.StartType != mgr.StartAutomatic || c.DelayedAutoStart != delayed || c.ServiceStartName != `NT SERVICE\`+name {
		return fmt.Errorf("service startup/account incorrect: %s", name)
	}
	if name == appService && (len(c.Dependencies) != 1 || c.Dependencies[0] != mysqlService) {
		return fmt.Errorf("MySQL dependency missing")
	}
	return waitService(s, svc.Running, 10*time.Second)
}

func restartTestServices() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	a, err := m.OpenService(appService)
	if err != nil {
		return err
	}
	defer a.Close()
	db, err := m.OpenService(mysqlService)
	if err != nil {
		return err
	}
	defer db.Close()
	stopService(a)
	stopService(db)
	if err = db.Start(); err != nil {
		return err
	}
	if err = waitService(db, svc.Running, 60*time.Second); err != nil {
		return err
	}
	if err = a.Start(); err != nil {
		return err
	}
	return waitService(a, svc.Running, 60*time.Second)
}

func verifyUpload(t *testing.T, request func(string, string, io.Reader, *http.Cookie, string) []byte, cookie *http.Cookie, csrf string) {
	t.Helper()
	data := []byte("GmPan real Windows service upload and scanner verification\n")
	payload, _ := json.Marshal(map[string]any{"scope": "personal", "name": "service-test-" + randomSecret()[:8] + ".txt", "remark": "installer verification", "size": len(data)})
	var init struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(request("POST", "/api/uploads", bytes.NewReader(payload), cookie, csrf), &init); err != nil || init.ID == "" {
		t.Fatal("upload initialization failed")
	}
	request("PUT", "/api/uploads/"+init.ID+"/chunks/0", bytes.NewReader(data), cookie, csrf)
	var entry struct {
		ID         string `json:"id"`
		ScanStatus string `json:"scan_status"`
	}
	if err := json.Unmarshal(request("POST", "/api/uploads/"+init.ID+"/complete", nil, cookie, csrf), &entry); err != nil || entry.ID == "" {
		t.Fatal("upload completion failed")
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		b := request("GET", "/api/files/"+entry.ID, nil, cookie, csrf)
		var detail struct {
			ScanStatus string `json:"scan_status"`
		}
		if err := json.Unmarshal(b, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ScanStatus == "clean" {
			if got := request("GET", "/api/files/"+entry.ID+"/content", nil, cookie, csrf); !bytes.Equal(got, data) {
				t.Fatal("downloaded data differs")
			}
			return
		}
		if detail.ScanStatus == "failed" || detail.ScanStatus == "infected" {
			t.Fatalf("service scanner status=%s", detail.ScanStatus)
		}
		time.Sleep(time.Second)
	}
	t.Fatal("service scanner did not finish")
}

func checkInstalledSiteRestart(t *testing.T, address, user, password, certPath string) string {
	t.Helper()
	transport := &http.Transport{}
	if certPath != "" {
		b, err := os.ReadFile(certPath)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			t.Fatal("invalid certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	loginBody, _ := json.Marshal(map[string]string{"account": user, "password": password})
	res, err := client.Post(address+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	var userReply struct {
		CSRF string `json:"csrf"`
	}
	json.NewDecoder(res.Body).Decode(&userReply)
	res.Body.Close()
	if res.StatusCode != 200 || len(res.Cookies()) == 0 {
		t.Fatal("restart test login failed")
	}
	cookie := res.Cookies()[0]
	request := func(method, path string, body io.Reader, cookie *http.Cookie, csrf string) []byte {
		r, err := http.NewRequest(method, address+path, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("restart test %s status %d: %s", path, res.StatusCode, b)
		}
		return b
	}
	var state struct {
		Settings struct {
			Revision int64 `json:"revision"`
		} `json:"settings"`
		Instance string `json:"instance_id"`
	}
	json.Unmarshal(request("GET", "/api/admin/site-settings", nil, cookie, userReply.CSRF), &state)
	previous := state.Instance
	newPort := freePort(18410)
	body, _ := json.Marshal(map[string]any{"port": newPort, "revision": state.Settings.Revision})
	json.Unmarshal(request("PUT", "/api/admin/listen-port", bytes.NewReader(body), cookie, userReply.CSRF), &state)
	body, _ = json.Marshal(map[string]any{"revision": state.Settings.Revision})
	request("POST", "/api/admin/restart", bytes.NewReader(body), cookie, userReply.CSRF)
	old := address
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(newPort))
	address = parsed.String()
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		res, err := client.Get(address + "/api/health")
		if err == nil {
			var reply struct {
				Status   string `json:"status"`
				Instance string `json:"instance_id"`
			}
			json.NewDecoder(res.Body).Decode(&reply)
			res.Body.Close()
			if res.StatusCode == 200 && reply.Status == "ok" && reply.Instance != previous {
				ready = true
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Windows service site restart/new listener did not become healthy")
	}
	request("GET", "/api/me", nil, cookie, userReply.CSRF)
	verifyUpload(t, request, cookie, userReply.CSRF)
	if res, err := client.Get(old + "/api/health"); err == nil {
		res.Body.Close()
		t.Fatal("old listener remained after port change")
	}
	t.Log("Windows service: API site restart, changed HTTPS/HTTP port, preserved session and real scanner verified")
	return address
}
