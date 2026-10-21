package app

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSMSAdministratorDiagnostics(t *testing.T) {
	for _, item := range []struct {
		name, body, advice string
		status             int
	}{
		{"signature", `{"code":20001,"msg":"private-secret 13800000000 123456","data":{"code":"123456"}}`, "签名校验失败", 200},
		{"clock", `{"code":20002}`, "校准部署服务器时间", 200},
		{"whitelist", `{"code":20005}`, "出口公网 IP", 403},
		{"quota", `{"code":30001}`, "额度", 200},
		{"scene", `{"code":10001}`, "场景标识", 200},
		{"template", `{"code":40003}`, "模板授权", 200},
		{"unknown", `{"code":98765,"msg":"private-secret"}`, "98765", 200},
		{"html", `<html>private-secret</html>`, "HTTP 502", 502},
		{"missing-code", `{"msg":"private-secret"}`, "code=0", 200},
	} {
		t.Run(item.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(item.status)
				io.WriteString(w, item.body)
			}))
			defer provider.Close()
			s := &Server{smsClient: provider.Client()}
			err := s.smsRequestWith(SMSConfig{BaseURL: provider.URL, AppKey: "private-key", AppSecret: "private-secret"}, "/verify-code/send", map[string]string{"scene": "login", "mobile": "13800000000"})
			var detail *smsRequestError
			var public apiError
			if !errors.As(err, &detail) || !errors.As(err, &public) || public.Status != 502 {
				t.Fatalf("error does not preserve the public error boundary: %v", err)
			}
			message := detail.diagnostic()
			if !strings.Contains(message, item.advice) {
				t.Fatalf("missing administrator advice: %s", message)
			}
			for _, private := range []string{"private-secret", "private-key", "13800000000", "123456"} {
				if strings.Contains(message, private) || strings.Contains(public.Message, private) {
					t.Fatal("provider response leaked private data")
				}
			}
		})
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		io.WriteString(w, `{"code":0}`)
	}))
	defer provider.Close()
	client := provider.Client()
	client.Timeout = 10 * time.Millisecond
	s := &Server{smsClient: client}
	var detail *smsRequestError
	if err := s.smsRequestWith(SMSConfig{BaseURL: provider.URL}, "/verify-code/send", nil); !errors.As(err, &detail) || !strings.Contains(detail.diagnostic(), "超时") {
		t.Fatal("timeout must report uncertain acceptance without retrying")
	}
}
