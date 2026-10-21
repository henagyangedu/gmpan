package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordPolicy(t *testing.T) {
	for _, value := range []string{"abcd1234", "ABCD1234", "Ab12!@34", "ab12中文测试", strings.Repeat("a", 71) + "1"} {
		if !ValidPassword(value) {
			t.Fatal("valid password rejected")
		}
	}
	for _, value := range []string{"", "abc1234", "abcdefgh", "12345678", "!@#$%^&*", "a1中文测试题", strings.Repeat("a", 72) + "1", "abc1234\n", "abc1234\r", "abc1234\x00", "abc1234\xff"} {
		if ValidPassword(value) {
			t.Fatal("invalid password accepted")
		}
	}
}

func TestIntegrationEightCharacterPasswordLifecycle(t *testing.T) {
	s, clients := testServer(t)
	input := userInput{Username: "eightchar", DisplayName: "八位密码测试", Password: "abcd1234", Role: "member", Enabled: true, QuotaBytes: 1 << 20}
	for _, invalid := range []string{"abc1234", "abcdefgh", "12345678"} {
		input.Password = invalid
		expectStatus(t, call(t, s, clients["admin"], "POST", "/api/admin/users", input), 400)
	}
	input.Password = "abcd1234"
	created := call(t, s, clients["admin"], "POST", "/api/admin/users", input)
	expectStatus(t, created, 201)
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatal("new employee ID missing")
	}
	login := call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": input.Username, "password": input.Password})
	expectStatus(t, login, 200)
	var user User
	if err := json.Unmarshal(login.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	employee := testClient{cookie: login.Result().Cookies()[0], csrf: user.CSRF, user: &user}
	for _, invalid := range []string{"abc1234", "abcdefgh", "12345678"} {
		expectStatus(t, call(t, s, employee, "POST", "/api/auth/password", map[string]string{"old_password": "abcd1234", "new_password": invalid}), 400)
		input.Password = invalid
		expectStatus(t, call(t, s, clients["admin"], "PATCH", "/api/admin/users/"+result.ID, input), 400)
	}
	expectStatus(t, call(t, s, employee, "POST", "/api/auth/password", map[string]string{"old_password": "abcd1234", "new_password": "next1234"}), 200)
	expectStatus(t, call(t, s, employee, "GET", "/api/me", nil), 401)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": input.Username, "password": "next1234"}), 200)
	input.Password = "reset123"
	expectStatus(t, call(t, s, clients["admin"], "PATCH", "/api/admin/users/"+result.ID, input), 200)
	input.Password = ""
	expectStatus(t, call(t, s, clients["admin"], "PATCH", "/api/admin/users/"+result.ID, input), 200)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": input.Username, "password": "reset123"}), 200)
	if err := ResetAdminPassword(context.Background(), s.DB, "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "admin", "password": "admin123"}), 200)
	// The creation policy must not retroactively lock out existing passwords.
	hash, err := bcrypt.GenerateFromPassword([]byte("legacyPassword"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE users SET password_hash=? WHERE id=?", string(hash), result.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": input.Username, "password": "legacyPassword"}), 200)
}

func TestIntegrationFirstAdminEightCharacterPassword(t *testing.T) {
	s, _ := testServer(t)
	// testServer creates a dedicated disposable database at 127.0.0.1:13386.
	for _, query := range []string{"DELETE FROM sessions", "DELETE FROM users"} {
		if _, err := s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{"abc1234", "abcdefgh", "12345678"} {
		if err := CreateAdmin(s.DB, "firstadmin", invalid, 1<<20); err == nil {
			t.Fatal("first-admin setup accepted an invalid password")
		}
	}
	if err := CreateAdmin(s.DB, "firstadmin", "admin123", 1<<20); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "firstadmin", "password": "admin123"}), 200)
}
