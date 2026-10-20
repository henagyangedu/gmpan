package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
)

func TestIntegrationBatchTrashAtomicAndRecursive(t *testing.T) {
	s, c := testServer(t)
	a := testFolder(t, s, c["alice"], "personal", "", "目录 A")
	b := testFolder(t, s, c["alice"], "personal", "", "目录 B")
	sub := testFolder(t, s, c["alice"], "personal", a, "子目录")
	nested := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal", "parent_id": sub})
	file := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal"})
	insertEntry(t, s, "previous-trash", "personal", "alice", "", "old.txt", "")
	s.DB.Exec("UPDATE entries SET parent_id=?,deleted=1,trash_batch='older-batch' WHERE id='previous-trash'", a)
	endpoint := "/api/files/batch-trash"
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, map[string]any{"ids": []string{file, nested}}), 400)
	response := call(t, s, c["maint"], "POST", endpoint, map[string]any{"ids": []string{a, b, file}})
	expectStatus(t, response, 200)
	var result map[string]int
	json.Unmarshal(response.Body.Bytes(), &result)
	if result["selected"] != 3 || result["count"] != 5 {
		t.Fatal("wrong recursive count", result)
	}
	for _, id := range []string{a, b, sub, nested, file} {
		entry, e := s.getEntry(id, c["alice"].user)
		if e != nil || !entry.Deleted {
			t.Fatal("batch omitted child", id)
		}
	}
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/files/"+a+"/restore", nil), 200)
	for _, id := range []string{a, sub, nested} {
		entry, _ := s.getEntry(id, c["alice"].user)
		if entry.Deleted {
			t.Fatal("restore omitted child", id)
		}
	}
	for _, id := range []string{b, file, "previous-trash"} {
		entry, _ := s.getEntry(id, c["alice"].user)
		if !entry.Deleted {
			t.Fatal("restore expanded outside directory or into earlier trash", id)
		}
	}
	// A descendant owned by another employee prevents the whole member deletion.
	public := testFolder(t, s, c["alice"], "company", "", "共享目录")
	insertEntry(t, s, "foreign-child", "company", "bob", "", "foreign.txt", "")
	s.DB.Exec("UPDATE entries SET parent_id=? WHERE id='foreign-child'", public)
	insertEntry(t, s, "own-root", "company", "alice", "", "own.txt", "")
	ids := map[string]any{"ids": []string{public, "own-root"}}
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, ids), 400)
	for _, id := range []string{public, "foreign-child", "own-root"} {
		entry, _ := s.getEntry(id, c["admin"].user)
		if entry.Deleted {
			t.Fatal("permission failure partially deleted", id)
		}
	}
	expectStatus(t, call(t, s, c["maint"], "POST", endpoint, ids), 200)
}
func TestIntegrationBatchDownloadOnlySameDirectoryFiles(t *testing.T) {
	s, c := testServer(t)
	s.scan = cleanTestScanner{}
	first := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal"})
	expectStatus(t, call(t, s, c["alice"], "PATCH", "/api/files/"+first, map[string]string{"name": "first.txt", "remark": ""}), 200)
	second := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal"})
	for _, id := range []string{first, second} {
		s.scanOne(context.Background(), id)
	}
	folder := testFolder(t, s, c["alice"], "personal", "", "目录")
	nested := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal", "parent_id": folder})
	s.scanOne(context.Background(), nested)
	endpoint := "/api/files/batch-download"
	for _, ids := range [][]string{{}, {first, first}, {folder}, {first, folder}, {first, nested}} {
		expectStatus(t, call(t, s, c["alice"], "POST", endpoint, map[string]any{"ids": ids}), 400)
	}
	ids := map[string]any{"ids": []string{first, second}}
	expectStatus(t, call(t, s, c["bob"], "POST", endpoint, ids), 404)
	w := call(t, s, c["maint"], "POST", endpoint, ids)
	expectStatus(t, w, 201)
	var job struct {
		URL string `json:"url"`
	}
	json.Unmarshal(w.Body.Bytes(), &job)
	w = call(t, s, c["maint"], "GET", job.URL, nil)
	expectStatus(t, w, 200)
	z, e := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if e != nil {
		t.Fatal(e)
	}
	if len(z.File) != 2 {
		t.Fatal("ZIP must contain only two selected files")
	}
	for _, f := range z.File {
		if f.Name != "first.txt" && f.Name != "managed.txt" {
			t.Fatal("directory or unselected file leaked", f.Name)
		}
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(r)
		r.Close()
		if e != nil || string(data) != "test" {
			t.Fatal("ZIP CRC or content error", e)
		}
	}
	s.DB.Exec("UPDATE entries SET size=? WHERE id=?", folderMaxBytes, first)
	expectStatus(t, call(t, s, c["admin"], "POST", endpoint, ids), 400)
	if s.archiveBusy {
		t.Fatal("rejected batch kept archive lock")
	}
}
