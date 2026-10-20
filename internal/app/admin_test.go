package app

import (
	"encoding/json"
	"testing"
)

func TestIntegrationEmployeeLifecycleAndSessionRevocation(t *testing.T) {
	s, c := testServer(t)
	input := userInput{Username: "charlie", DisplayName: "测试员工", Mobile: "13900000000", Password: "InitialPass2026", Role: "member", DeptID: "sales", Enabled: true, QuotaBytes: 1 << 20}
	created := call(t, s, c["admin"], "POST", "/api/admin/users", input)
	expectStatus(t, created, 201)
	var value map[string]string
	json.Unmarshal(created.Body.Bytes(), &value)
	id := value["id"]
	input.Username = "dave"
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/admin/users", input), 409)
	input.Username = "charlie"
	login := call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "charlie", "password": "InitialPass2026"})
	expectStatus(t, login, 200)
	var u User
	json.Unmarshal(login.Body.Bytes(), &u)
	if u.DeptID != "sales" {
		t.Fatal("department not assigned")
	}
	employee := testClient{cookie: login.Result().Cookies()[0], csrf: u.CSRF, user: &u}
	input.DeptID = "finance"
	input.Password = "ResetPassword2026"
	expectStatus(t, call(t, s, c["admin"], "PATCH", "/api/admin/users/"+id, input), 200)
	expectStatus(t, call(t, s, employee, "GET", "/api/me", nil), 401)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "charlie", "password": "InitialPass2026"}), 401)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "13900000000", "password": "ResetPassword2026"}), 200)
	input.Enabled = false
	expectStatus(t, call(t, s, c["admin"], "PATCH", "/api/admin/users/"+id, input), 200)
	expectStatus(t, call(t, s, testClient{}, "POST", "/api/auth/login", map[string]string{"account": "charlie", "password": "ResetPassword2026"}), 401)
	input = userInput{Username: "admin", DisplayName: "admin", Role: "admin", Enabled: false, QuotaBytes: 20 << 30}
	expectStatus(t, call(t, s, c["admin"], "PATCH", "/api/admin/users/admin", input), 400)
}

func TestIntegrationRestorePreservesEarlierTrash(t *testing.T) {
	s, c := testServer(t)
	folder := call(t, s, c["alice"], "POST", "/api/folders", map[string]string{"scope": "personal", "name": "项目目录"})
	expectStatus(t, folder, 201)
	var p map[string]string
	json.Unmarshal(folder.Body.Bytes(), &p)
	parent := p["id"]
	for _, id := range []string{"first", "second"} {
		insertEntry(t, s, id, "personal", "alice", "", id+".txt", "")
		if _, e := s.DB.Exec("UPDATE entries SET parent_id=? WHERE id=?", parent, id); e != nil {
			t.Fatal(e)
		}
	}
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/files/first/trash", nil), 200)
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/files/"+parent+"/trash", nil), 200)
	expectStatus(t, call(t, s, c["alice"], "POST", "/api/files/"+parent+"/restore", nil), 200)
	var firstDeleted, secondDeleted bool
	s.DB.QueryRow("SELECT deleted FROM entries WHERE id='first'").Scan(&firstDeleted)
	s.DB.QueryRow("SELECT deleted FROM entries WHERE id='second'").Scan(&secondDeleted)
	if !firstDeleted || secondDeleted {
		t.Fatal("restoring a folder restored independently trashed content")
	}
}
