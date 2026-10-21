package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigIgnoresLegacySMS(t *testing.T) {
	t.Setenv("GMPAN_DSN", "")
	t.Setenv("GMPAN_SMS_KEY", "legacy-key")
	t.Setenv("GMPAN_SMS_SECRET", "legacy-secret")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mysql_dsn":"gmpan:placeholder@tcp(127.0.0.1:3306)/gmpan","sms":{"base_url":"invalid-old-address","app_key":"legacy-key","app_secret":"legacy-secret","login_scene":"legacy.login"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sms") || strings.Contains(string(b), "legacy-secret") {
		t.Fatal("deployment configuration retains SMS settings")
	}
	if _, enabled := (&Server{Config: c}).smsSnapshot(); enabled {
		t.Fatal("SMS enabled without saved backend settings")
	}
}

func TestConfigLANHTTPRequiresExplicitChoice(t *testing.T) {
	t.Setenv("GMPAN_DSN", "")
	path := filepath.Join(t.TempDir(), "config.json")
	for _, item := range []struct {
		listen string
		allow  bool
		cert   string
		key    string
		ok     bool
	}{
		{"192.168.2.92:80", true, "", "", true},
		{"192.168.2.92:80", false, "", "", false},
		{"0.0.0.0:80", true, "", "", true},
		{"203.0.113.8:80", true, "", "", false},
		{"intranet.example:80", true, "", "", false},
		{"127.0.0.1:80", false, "", "", true},
		{"192.168.2.92:443", false, "server.crt", "server.key", true},
		{"192.168.2.92:443", true, "server.crt", "", false},
	} {
		b, _ := json.Marshal(Config{Listen: item.listen, DSN: "gmpan:placeholder@tcp(127.0.0.1:3306)/gmpan", AllowLANHTTP: item.allow, TLSCert: item.cert, TLSKey: item.key, MaxFileBytes: 1, PersonalQuotaBytes: 1})
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadConfig(path)
		if (err == nil) != item.ok {
			t.Fatalf("listen=%s allow=%v result=%v", item.listen, item.allow, err)
		}
		if err == nil && c.AllowLANHTTP != item.allow {
			t.Fatal("explicit HTTP preference not retained")
		}
	}
}
