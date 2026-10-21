package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTemplateSMSCodeLifecycle(t *testing.T) {
	var captured string
	var fail bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Variables map[string]string `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		captured = payload.Variables["code"]
		if fail {
			io.WriteString(w, `{"code":20001}`)
		} else {
			io.WriteString(w, `{"code":0}`)
		}
	}))
	defer provider.Close()
	s := &Server{smsClient: provider.Client(), smsSettings: SMSSettings{SMSConfig: SMSConfig{BaseURL: provider.URL, AppKey: "mock", AppSecret: "mock-secret", TemplateKey: "standard_code"}, Enabled: true, Revision: 1}}
	config, _ := s.smsSnapshot()
	issue := func() string {
		t.Helper()
		if err := s.sendLoginCode(config, "13800000000", "alice"); err != nil {
			t.Fatal(err)
		}
		return captured
	}
	code := issue()
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Fatal("invalid code format")
	}
	if s.smsCodes["13800000000"].hash == code {
		t.Fatal("plaintext code retained")
	}
	if s.consumeSMSCode("13800000000", "bob", code) {
		t.Fatal("code transferred to a different account")
	}
	var accepted atomic.Int32
	var calls sync.WaitGroup
	for i := 0; i < 20; i++ {
		calls.Go(func() {
			if s.consumeSMSCode("13800000000", "alice", code) {
				accepted.Add(1)
			}
		})
	}
	calls.Wait()
	if accepted.Load() != 1 {
		t.Fatal("concurrent code replay accepted")
	}
	code = issue()
	wrong := "0" + code[1:]
	if wrong == code {
		wrong = "1" + code[1:]
	}
	for i := 0; i < 5; i++ {
		if s.consumeSMSCode("13800000000", "alice", wrong) {
			t.Fatal("wrong code accepted")
		}
	}
	if s.consumeSMSCode("13800000000", "alice", code) {
		t.Fatal("five failed attempts did not invalidate the code")
	}
	code = issue()
	v := s.smsCodes["13800000000"]
	v.expires = time.Now().Add(-time.Second)
	s.smsCodes["13800000000"] = v
	if s.consumeSMSCode("13800000000", "alice", code) {
		t.Fatal("expired code accepted")
	}
	old := issue()
	code = issue()
	if old != code && s.consumeSMSCode("13800000000", "alice", old) {
		t.Fatal("old code survived replacement")
	}
	if !s.consumeSMSCode("13800000000", "alice", code) {
		t.Fatal("replacement code rejected")
	}
	code = issue()
	s.smsSettings.Revision++
	if s.consumeSMSCode("13800000000", "alice", code) {
		t.Fatal("code survived a configuration revision change")
	}
	fail = true
	if err := s.sendLoginCode(config, "13800000000", "alice"); err == nil {
		t.Fatal("provider rejection accepted")
	}
	if len(s.smsCodes) != 0 {
		t.Fatal("failed send retained an active code")
	}
}

func TestTemplateSMSPendingCannotLogin(t *testing.T) {
	received, release := make(chan string, 1), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]string `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		received <- body.Variables["code"]
		<-release
		io.WriteString(w, `{"code":0}`)
	}))
	defer provider.Close()
	s := &Server{smsClient: provider.Client(), smsSettings: SMSSettings{SMSConfig: SMSConfig{BaseURL: provider.URL, AppKey: "mock", AppSecret: "mock-secret", TemplateKey: "standard_code"}, Enabled: true, Revision: 1}}
	config, _ := s.smsSnapshot()
	done := make(chan error, 1)
	go func() { done <- s.sendLoginCode(config, "13800000000", "alice") }()
	code := <-received
	if s.consumeSMSCode("13800000000", "alice", code) {
		t.Error("code usable before provider acceptance")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !s.consumeSMSCode("13800000000", "alice", code) {
		t.Fatal("accepted code not activated")
	}
}

func TestIntegrationTemplateSMSLogin(t *testing.T) {
	s, clients := testServer(t)
	var code string
	var sends atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		var body struct {
			TemplateKey string            `json:"templateKey"`
			Mobile      string            `json:"mobile"`
			Variables   map[string]string `json:"variables"`
			RequestID   string            `json:"requestId"`
		}
		if r.URL.Path != "/sms/send" || json.NewDecoder(r.Body).Decode(&body) != nil || body.TemplateKey != "standard_code" || len(body.RequestID) != 32 {
			t.Error("unexpected platform request")
		}
		code = body.Variables["code"]
		io.WriteString(w, `{"code":0}`)
	}))
	defer provider.Close()
	s.smsClient = provider.Client()
	saveTestSMS(t, s, clients["admin"], provider.URL, true)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13900000000"}), 200)
	if sends.Load() != 0 {
		t.Fatal("unbound phone received a login code")
	}
	if _, err := s.DB.Exec("UPDATE users SET mobile='13800000000' WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}), 200)
	if sends.Load() != 1 {
		t.Fatal("login SMS was not sent exactly once")
	}
	wrong := "0" + code[1:]
	if wrong == code {
		wrong = "1" + code[1:]
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/login", map[string]string{"mobile": "13800000000", "code": wrong}), 401)
	w := call(t, s, testClient{}, "POST", "/api/auth/sms/login", map[string]string{"mobile": "13800000000", "code": code})
	expectStatus(t, w, 200)
	if len(w.Result().Cookies()) != 1 || !strings.Contains(w.Body.String(), `"id":"alice"`) {
		t.Fatal("successful code did not create the correct session")
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/login", map[string]string{"mobile": "13800000000", "code": code}), 401)
	if sends.Load() != 1 {
		t.Fatal("verification unexpectedly contacted the SMS platform")
	}
	if _, err := s.DB.Exec("UPDATE sms_cooldowns SET sent_at=DATE_SUB(NOW(),INTERVAL 61 SECOND)"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/admin/sms-test", nil), 401)
	expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/sms-test", map[string]any{"mobile": "13800000000", "revision": s.smsSettings.Revision}), 200)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/login", map[string]string{"mobile": "13800000000", "code": code}), 401)
	if sends.Load() != 2 {
		t.Fatal("administrator test did not send once")
	}
	if _, err := s.DB.Exec("UPDATE sms_cooldowns SET sent_at=DATE_SUB(NOW(),INTERVAL 61 SECOND)"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/send", map[string]string{"mobile": "13800000000"}), 200)
	if _, err := s.DB.Exec("UPDATE users SET enabled=0 WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/sms/login", map[string]string{"mobile": "13800000000", "code": code}), 401)
}

func TestIntegrationSMSTemplateUpgrade(t *testing.T) {
	s, _ := testServer(t)
	legacy := `{"base_url":"https://sms.example.test/api/v1","app_key":"mock-key","app_secret":"mock-secret","login_scene":"standard_code","enabled":true}`
	if _, err := s.DB.Exec("UPDATE sms_settings SET settings=?,revision=7 WHERE id=1", legacy); err != nil {
		t.Fatal(err)
	}
	v, err := loadSMSSettings(s.DB)
	if err != nil || v.TemplateKey != "standard_code" || v.Revision != 7 || v.AppSecret != "mock-secret" {
		t.Fatal("upgrade lost saved SMS configuration")
	}
}
