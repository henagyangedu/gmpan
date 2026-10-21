package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func saveTestSMS(t *testing.T, s *Server, client testClient, baseURL string, enabled bool) {
	t.Helper()
	expectStatus(t, call(t, s, client, "PUT", "/api/admin/sms-settings", map[string]any{"enabled": enabled, "base_url": baseURL, "app_key": "mock-key", "app_secret": "mock-secret", "template_key": "standard_code", "revision": s.smsSettings.Revision}), 200)
}

func TestIntegrationAdministratorSMSTest(t *testing.T) {
	s, clients := testServer(t)
	var requests, platformCode, httpStatus atomic.Int32
	httpStatus.Store(200)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			TemplateKey string            `json:"templateKey"`
			Mobile      string            `json:"mobile"`
			Variables   map[string]string `json:"variables"`
			RequestID   string            `json:"requestId"`
		}
		if r.URL.Path != "/sms/send" || json.Unmarshal(body, &payload) != nil || payload.TemplateKey != "standard_code" || !mobilePattern.MatchString(payload.Mobile) || len(payload.Variables["code"]) != 6 || len(payload.RequestID) != 32 {
			t.Error("test send did not use the saved template-send contract")
		}
		if r.Header.Get("X-Gm-AppKey") != "mock-key" || r.Header.Get("X-Gm-Sign") != SMSSign("mock-key", "mock-secret", r.Header.Get("X-Gm-Timestamp"), r.Header.Get("X-Gm-Nonce"), body) {
			t.Error("test send did not use the saved credentials and exact body signature")
		}
		w.WriteHeader(int(httpStatus.Load()))
		if httpStatus.Load() == 502 {
			io.WriteString(w, "<html>mock-secret</html>")
		} else {
			fmt.Fprintf(w, `{"code":%d,"msg":"mock-key mock-secret 13800000000 123456","data":{"code":"123456"}}`, platformCode.Load())
		}
	}))
	defer provider.Close()
	s.smsClient = provider.Client()
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	payload := map[string]any{"mobile": "13800000000", "revision": s.smsSettings.Revision}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/admin/sms-test", payload), 401)
	for _, role := range []string{"maint", "alice"} {
		expectStatus(t, call(t, s, clients[role], "POST", "/api/admin/sms-test", payload), 403)
	}
	withoutCSRF := clients["admin"]
	withoutCSRF.csrf = ""
	expectStatus(t, call(t, s, withoutCSRF, "POST", "/api/admin/sms-test", payload), 403)
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 400)
	saveTestSMS(t, s, clients["admin"], provider.URL, false)
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 409)
	payload["revision"] = s.smsSettings.Revision
	payload["mobile"] = "123"
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 400)
	payload["mobile"] = "13800000000"
	payload["app_secret"] = "unsaved-value"
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 400)
	delete(payload, "app_secret")
	if requests.Load() != 0 {
		t.Fatal("invalid, stale or unauthorized test sent an SMS")
	}
	response := call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload)
	expectStatus(t, response, 200)
	var result struct {
		Accepted bool `json:"accepted"`
		Code     int  `json:"platform_code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Accepted || result.Code != 0 || requests.Load() != 1 {
		t.Fatal("configured test did not receive an accepted response")
	}
	if s.smsEnabled() {
		t.Fatal("testing while disabled unexpectedly enabled employee SMS login")
	}
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 429)
	saveTestSMS(t, s, clients["admin"], provider.URL, true)
	payload["revision"] = s.smsSettings.Revision
	if _, err := s.DB.Exec("UPDATE users SET mobile='13800000000' WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}), 429)
	if _, err := s.DB.Exec("UPDATE sms_cooldowns SET sent_at=DATE_SUB(NOW(),INTERVAL 61 SECOND)"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}), 200)
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 429)
	for i, code := range []int32{20001, 30001, 98765, 0} {
		platformCode.Store(code)
		if i == 3 {
			httpStatus.Store(502)
		}
		payload["mobile"] = fmt.Sprintf("1390000000%d", i)
		response = call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload)
		expectStatus(t, response, 200)
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Accepted || (i < 3 && result.Code != int(code)) {
			t.Fatal("platform refusal must remain a failed test with a diagnostic code")
		}
		for _, private := range []string{"mock-key", "mock-secret", "13800000000", "123456"} {
			if strings.Contains(response.Body.String(), private) {
				t.Fatal("test response leaked provider data")
			}
		}
	}
	before := requests.Load()
	payload["mobile"] = "13900000009"
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", payload), 429)
	if requests.Load() != before {
		t.Fatal("administrator test limit did not stop delivery")
	}
	var users, sessions, audits int
	s.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	s.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions)
	s.DB.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action='sms_test'").Scan(&audits)
	if users != 5 || sessions != 5 || audits != 5 {
		t.Fatal("test changed accounts/sessions or failed to audit sends")
	}
	for _, private := range []string{"mock-key", "mock-secret", "13800000000", "123456"} {
		if strings.Contains(logs.String(), private) {
			t.Fatal("test log leaked private data")
		}
	}
}

// Explicit opt-in only: one real send using temporary environment credentials.
// Never store the live credentials in a fixture, database, response or log.
func TestLiveAdministratorSMSTestOnce(t *testing.T) {
	if os.Getenv("GMPAN_SMS_LIVE_CONFIRM") != "SEND_ONE" {
		t.Skip("real SMS requires explicit recipient and one-message authorization")
	}
	key, secret := os.Getenv("GMPAN_SMS_LIVE_KEY"), os.Getenv("GMPAN_SMS_LIVE_SECRET")
	phone, scene := os.Getenv("GMPAN_SMS_LIVE_MOBILE"), os.Getenv("GMPAN_SMS_LIVE_TEMPLATE")
	if key == "" || secret == "" || scene == "" || !mobilePattern.MatchString(phone) {
		t.Fatal("live SMS configuration is incomplete; no message sent")
	}
	s, clients := testServer(t)
	s.smsSettings.SMSConfig = SMSConfig{BaseURL: "https://sms.gmwang.com.cn/api/v1", AppKey: key, AppSecret: secret, TemplateKey: scene}
	marker, err := os.OpenFile(os.Getenv("GMPAN_SMS_LIVE_MARKER"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("one-send marker unavailable or already present; refusing a second send")
	}
	marker.Close()
	response := call(t, s, clients["admin"], "POST", "/api/admin/sms-test", map[string]any{"mobile": phone, "revision": s.smsSettings.Revision})
	for _, private := range []string{key, secret, phone} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("live response contained private data")
		}
	}
	expectStatus(t, response, 200)
	if err := os.WriteFile(os.Getenv("GMPAN_SMS_LIVE_RESULT"), response.Body.Bytes(), 0600); err != nil {
		t.Fatal("cannot record sanitized SMS diagnostic result")
	}
	t.Logf("one-message diagnostic: %s", response.Body.String())
}

func TestIntegrationAdministratorSMSCooldown(t *testing.T) {
	s, clients := testServer(t)
	var requests atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"code":0}`)
	}))
	defer provider.Close()
	s.smsClient = provider.Client()
	saveTestSMS(t, s, clients["admin"], provider.URL, true)
	if _, err := s.DB.Exec("UPDATE users SET mobile='13800000000' WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	start, statuses := make(chan struct{}), make(chan int, 2)
	go func() {
		<-start
		statuses <- call(t, s, clients["admin"], "POST", "/api/admin/sms-test", map[string]any{"mobile": "13800000000", "revision": s.smsSettings.Revision}).Code
	}()
	go func() {
		<-start
		statuses <- call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}).Code
	}()
	close(start)
	first, second := <-statuses, <-statuses
	if requests.Load() != 1 || !((first == 200 && second == 429) || (first == 429 && second == 200)) {
		t.Fatal("simultaneous login and administrator testing bypassed shared phone cooldown")
	}
}

// Optional local browser fixture; only a loopback mock provider is contacted.
func TestAdministratorSMSBrowserPreview(t *testing.T) {
	path := os.Getenv("GMPAN_SMS_UI_FIXTURE")
	if path == "" {
		t.Skip("local browser fixture not requested")
	}
	s, clients := testServer(t)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := os.Stat(path + ".fail"); err == nil {
			io.WriteString(w, `{"code":40001}`)
		} else {
			io.WriteString(w, `{"code":0}`)
		}
	}))
	defer provider.Close()
	s.smsClient = provider.Client()
	saveTestSMS(t, s, clients["admin"], provider.URL, false)
	preview := httptest.NewServer(s.Handler())
	defer preview.Close()
	if err := os.WriteFile(path, []byte(preview.URL), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("loopback-only SMS browser preview ready")
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path + ".stop"); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("browser preview fixture expired")
}
