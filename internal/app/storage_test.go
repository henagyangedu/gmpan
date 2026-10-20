package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func responseID(t *testing.T, body []byte) string {
	t.Helper()
	var value map[string]any
	if e := json.Unmarshal(body, &value); e != nil {
		t.Fatal(e)
	}
	id, _ := value["id"].(string)
	if id == "" {
		t.Fatal("missing id")
	}
	return id
}
func uploadTestFile(t *testing.T, s *Server, c testClient, target map[string]any) string {
	t.Helper()
	target["name"], target["size"] = "managed.txt", 4
	w := call(t, s, c, "POST", "/api/uploads", target)
	expectStatus(t, w, 201)
	id := responseID(t, w.Body.Bytes())
	expectStatus(t, call(t, s, c, "PUT", "/api/uploads/"+id+"/chunks/0", []byte("test")), 200)
	w = call(t, s, c, "POST", "/api/uploads/"+id+"/complete", nil)
	expectStatus(t, w, 201)
	return responseID(t, w.Body.Bytes())
}
func TestStorageWarningAndVerifiedCopy(t *testing.T) {
	if storageLow(10, 100, 10) || !storageLow(9, 100, 10) || storageLow(0, 0, 10) {
		t.Fatal("threshold boundary")
	}
	base := t.TempDir()
	source := filepath.Join(base, "source.txt")
	target := filepath.Join(base, "copied.txt")
	os.WriteFile(source, []byte("source"), 0600)
	if _, e := copyStorageFile(source, target, base); e != nil {
		t.Fatal(e)
	}
	if _, e := copyStorageFile(source, target, base); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(source, []byte("different"), 0600)
	if _, e := copyStorageFile(source, target, base); e == nil {
		t.Fatal("overwrote a different target")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "source" {
		t.Fatal("target changed")
	}
}
func TestIntegrationMaintenanceAccessAndTargets(t *testing.T) {
	s, c := testServer(t)
	insertEntry(t, s, "private", "personal", "alice", "", "private.txt", "private keyword")
	insertEntry(t, s, "dept", "department", "alice", "sales", "sales.txt", "department keyword")
	for _, id := range []string{"private", "dept"} {
		expectStatus(t, call(t, s, c["maint"], "GET", "/api/files/"+id, nil), 200)
		expectStatus(t, call(t, s, c["maint"], "PATCH", "/api/files/"+id, map[string]string{"name": id + ".txt", "remark": "updated by maintenance"}), 200)
		expectStatus(t, call(t, s, c["bob"], "GET", "/api/files/"+id, nil), 404)
	}
	for _, path := range []string{"/api/files?scope=personal&owner=alice&q=maintenance", "/api/files?scope=department&dept=sales&q=maintenance"} {
		w := call(t, s, c["maint"], "GET", path, nil)
		expectStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "updated by maintenance") {
			t.Fatal("targeted search omitted files")
		}
		expectStatus(t, call(t, s, c["bob"], "GET", path, nil), 403)
	}
	for _, endpoint := range []string{"site-settings", "sms-settings"} {
		expectStatus(t, call(t, s, c["maint"], "GET", "/api/admin/"+endpoint, nil), 403)
		expectStatus(t, call(t, s, c["maint"], "PUT", "/api/admin/"+endpoint, map[string]any{}), 403)
	}
	expectStatus(t, call(t, s, c["maint"], "DELETE", "/api/admin/site-logo", nil), 403)
	expectStatus(t, call(t, s, c["maint"], "PUT", "/api/admin/site-logo", []byte("image")), 403)
	input := userInput{Username: "extra", DisplayName: "Extra", Role: "admin", Password: "InitialPass2026", Enabled: true, QuotaBytes: 1024}
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/admin/users", input), 403)
	input.Username = "admin"
	input.Role = "member"
	expectStatus(t, call(t, s, c["maint"], "PATCH", "/api/admin/users/admin", input), 403)
	input.Username = "alice"
	input.Role = "admin"
	expectStatus(t, call(t, s, c["maint"], "PATCH", "/api/admin/users/alice", input), 403)
	input.Username = "extra"
	input.Role = "member"
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/admin/users", input), 201)
	folder := call(t, s, c["maint"], "POST", "/api/folders", map[string]any{"scope": "personal", "owner_id": "alice", "name": "managed folder"})
	expectStatus(t, folder, 201)
	parent := responseID(t, folder.Body.Bytes())
	id := uploadTestFile(t, s, c["maint"], map[string]any{"scope": "personal", "owner_id": "alice", "parent_id": parent})
	v, e := s.getEntry(id, c["alice"].user)
	if e != nil || v.OwnerID != "alice" {
		t.Fatal("wrong file ownership", e)
	}
	s.scan = cleanTestScanner{}
	s.scanOne(context.Background(), id)
	expectStatus(t, call(t, s, c["maint"], "GET", "/api/files/"+id+"/content", nil), 200)
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/files/"+parent+"/trash", nil), 200)
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/files/"+id+"/content", nil), 404)
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/files/"+parent+"/restore", nil), 200)
	expectStatus(t, call(t, s, c["alice"], "GET", "/api/files/"+id+"/content", nil), 200)
	s.DB.Exec("UPDATE users SET quota_bytes=1 WHERE id='alice'")
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/uploads", map[string]any{"scope": "personal", "owner_id": "alice", "name": "over.txt", "size": 2}), 400)
	status := call(t, s, c["alice"], "GET", "/api/storage-status", nil)
	expectStatus(t, status, 200)
	if strings.Contains(status.Body.String(), s.Config.DataDir) || !strings.Contains(status.Body.String(), `"threshold_percent":10`) {
		t.Fatal("storage status exposes paths or wrong default")
	}
	vsettings := readSettings(t, s, c["admin"])
	vsettings.StorageWarningPercent = 99
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/site-settings", vsettings), 200)
	if !strings.Contains(call(t, s, c["alice"], "GET", "/api/storage-status", nil).Body.String(), `"threshold_percent":99`) {
		t.Fatal("threshold not applied")
	}
}
func TestIntegrationUnifiedStorageMigration(t *testing.T) {
	s, c := testServer(t)
	original := filepath.Join(s.Config.LegacyRoot, "original.txt")
	os.WriteFile(original, []byte("company source"), 0600)
	runTestImport(t, s)
	s.DB.Exec("UPDATE entries SET remark='preserved note' WHERE storage_kind='legacy' AND kind='file'")
	file := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal"})
	w := call(t, s, c["alice"], "POST", "/api/uploads", map[string]any{"scope": "personal", "name": "resume.txt", "size": 4})
	expectStatus(t, w, 201)
	resume := responseID(t, w.Body.Bytes())
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/uploads/"+resume+"/chunks/0", []byte("test")), 200)
	oldConfig := s.Config
	lock, e := s.DB.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var acquired int
	lock.QueryRowContext(context.Background(), "SELECT GET_LOCK(CONCAT('gmpan_',MD5(DATABASE())),0)").Scan(&acquired)
	if acquired != 1 {
		t.Fatal("test lock failed")
	}
	if e = UnifyStorage(s.DB, s.Config, s.Config.LegacyRoot); e == nil {
		t.Fatal("running server lock ignored")
	}
	lock.ExecContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT('gmpan_',MD5(DATABASE())))")
	lock.Close()
	previous, e := s.getEntry(file, c["alice"].user)
	if e != nil {
		t.Fatal(e)
	}
	oldPath, e := s.entryPath(previous)
	if e != nil {
		t.Fatal(e)
	}
	changedTime := time.Unix(0, previous.SourceMtime).Add(time.Second)
	os.Chtimes(oldPath, changedTime, changedTime)
	if e = UnifyStorage(s.DB, s.Config, s.Config.LegacyRoot); e == nil {
		t.Fatal("migration accepted modified uploaded file")
	}
	if readSettings(t, s, c["admin"]).StorageRoot != "" {
		t.Fatal("failed migration switched settings")
	}
	oldTime := time.Unix(0, previous.SourceMtime)
	os.Chtimes(oldPath, oldTime, oldTime)
	if e = UnifyStorage(s.DB, s.Config, s.Config.LegacyRoot); e != nil {
		t.Fatal(e)
	}
	s, e = NewServer(s.DB, oldConfig, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	if s.Config.StorageRoot != "" && !sameDirectory(s.Config.StorageRoot, oldConfig.LegacyRoot) {
		t.Fatal("wrong root")
	}
	entry, e := s.getEntry(file, c["alice"].user)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(entry.StoragePath, "personal/alice/") {
		t.Fatal("personal storage not isolated")
	}
	path, e := s.entryPath(entry)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "test" {
		t.Fatal("uploaded bytes changed")
	}
	if _, e = os.Stat(filepath.Join(oldConfig.DataDir, "objects")); e != nil {
		t.Fatal("old files removed")
	}
	w = call(t, s, c["alice"], "POST", "/api/uploads/"+resume+"/complete", nil)
	expectStatus(t, w, 201)
	var before, after int
	s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE storage_kind='legacy'").Scan(&before)
	runTestImport(t, s)
	s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE storage_kind='legacy'").Scan(&after)
	if before != after {
		t.Fatal("scan published internal storage or duplicated original files")
	}
	var notes int
	s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE remark='preserved note'").Scan(&notes)
	if notes != 1 {
		t.Fatal("remarks changed")
	}
	data, _ = os.ReadFile(original)
	if string(data) != "company source" {
		t.Fatal("original changed")
	}
	// Moving a unified root also preserves scope paths and original identifiers.
	next := t.TempDir()
	if e = UnifyStorage(s.DB, oldConfig, next); e != nil {
		t.Fatal(e)
	}
	s, e = NewServer(s.DB, oldConfig, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	entry, e = s.getEntry(file, c["alice"].user)
	if e != nil {
		t.Fatal(e)
	}
	path, e = s.entryPath(entry)
	if e != nil {
		t.Fatal(e)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "test" {
		t.Fatal("second migration failed")
	}
	if !sameDirectory(s.Config.StorageRoot, next) {
		t.Fatal("migrated setting not persisted")
	}
}
