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
