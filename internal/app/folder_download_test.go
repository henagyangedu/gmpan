package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testFolder(t *testing.T, s *Server, c testClient, scope, parent, name string) string {
	t.Helper()
	w := call(t, s, c, "POST", "/api/folders", map[string]string{"scope": scope, "parent_id": parent, "name": name})
	expectStatus(t, w, 201)
	return responseID(t, w.Body.Bytes())
}
func prepareTestArchive(t *testing.T, s *Server, c testClient, id string) string {
	t.Helper()
	w := call(t, s, c, "POST", "/api/folders/"+id+"/download", nil)
	expectStatus(t, w, 201)
	var result struct {
		URL string `json:"url"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return result.URL
}
func TestIntegrationFolderArchiveHierarchyAndAccess(t *testing.T) {
	s, c := testServer(t)
	s.scan = cleanTestScanner{}
	root := testFolder(t, s, c["alice"], "personal", "", "项目资料")
	sub := testFolder(t, s, c["alice"], "personal", root, "子目录")
	testFolder(t, s, c["alice"], "personal", root, "空目录")
	for _, parent := range []string{root, sub} {
		id := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal", "parent_id": parent})
		s.scanOne(context.Background(), id)
	}
	expectStatus(t, call(t, s, c["bob"], "POST", "/api/folders/"+root+"/download", nil), 404)
	noCSRF := c["alice"]
	noCSRF.csrf = ""
	expectStatus(t, call(t, s, noCSRF, "POST", "/api/folders/"+root+"/download", nil), 403)
	url := prepareTestArchive(t, s, c["maint"], root)
	expectStatus(t, call(t, s, c["alice"], "GET", url, nil), 404)
	expectStatus(t, call(t, s, c["admin"], "POST", "/api/folders/"+root+"/download", nil), 429)
	path := s.archiveJob.path
	w := call(t, s, c["maint"], "GET", url, nil)
	expectStatus(t, w, 200)
	if w.Header().Get("Content-Type") != "application/zip" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("wrong ZIP headers")
	}
	reader, e := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if e != nil {
		t.Fatal(e)
	}
	names := map[string]bool{}
	for _, f := range reader.File {
		names[f.Name] = true
		if !f.FileInfo().IsDir() {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(r)
			r.Close()
			if e != nil || string(data) != "test" {
				t.Fatal("ZIP bytes or CRC mismatch", e)
			}
		}
	}
	for _, name := range []string{"项目资料/", "项目资料/子目录/", "项目资料/空目录/", "项目资料/managed.txt", "项目资料/子目录/managed.txt"} {
		if !names[name] {
			t.Fatal("missing ZIP member", name)
		}
	}
	if len(names) != 5 {
		t.Fatal("unexpected ZIP members")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("ZIP not cleaned up")
	}
	expectStatus(t, call(t, s, c["maint"], "GET", url, nil), 404)
	// Publication filtering must not disclose another employee's pending content.
	public := testFolder(t, s, c["alice"], "company", "", "公司资料")
	file := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "company", "parent_id": public})
	s.scanOne(context.Background(), file)
	url = prepareTestArchive(t, s, c["bob"], public)
	w = call(t, s, c["bob"], "GET", url, nil)
	expectStatus(t, w, 200)
	reader, e = zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if e != nil || len(reader.File) != 1 {
		t.Fatal("pending file leaked in company ZIP")
	}
}
func TestIntegrationFolderArchiveSafetyAndLimits(t *testing.T) {
	s, c := testServer(t)
	s.scan = cleanTestScanner{}
	root := testFolder(t, s, c["alice"], "personal", "", "安全检查")
	file := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal", "parent_id": root})
	endpoint := "/api/folders/" + root + "/download"
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, nil), 423)
	s.scanOne(context.Background(), file)
	url := prepareTestArchive(t, s, c["alice"], root)
	s.DB.Exec("UPDATE entries SET deleted=1 WHERE id=?", file)
	expectStatus(t, call(t, s, c["alice"], "GET", url, nil), 409)
	s.DB.Exec("UPDATE entries SET deleted=0,size=? WHERE id=?", folderMaxBytes+1, file)
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, nil), 400)
	s.DB.Exec("UPDATE entries SET size=4 WHERE id=?", file)
	entry, _ := s.getEntry(file, c["alice"].user)
	path, _ := s.entryPath(entry)
	os.WriteFile(path, []byte("EDIT"), 0600)
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, nil), 423)
	if s.archiveBusy || s.archiveJob != nil {
		t.Fatal("failed archive kept lock or ticket")
	}
	remaining, _ := os.ReadDir(filepath.Join(s.Config.DataDir, "downloads"))
	if len(remaining) != 0 {
		t.Fatal("failed ZIP left temporary files")
	}
	// Check the hard file count without creating large physical files.
	s.DB.Exec("DELETE FROM entries WHERE id=?", file)
	tx, e := s.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i <= folderMaxFiles; i++ {
		name := fmt.Sprintf("file-%04d.txt", i)
		_, e = tx.Exec("INSERT INTO entries(id,parent_id,space_key,scope,owner_id,kind,name,name_hash,remark,size,scan_status) VALUES(?,?,?,'personal','alice','file',?,?,'',0,'clean')", newID(), root, "personal:alice", name, digest(name))
		if e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, nil), 400)
	s.DB.Exec("DELETE FROM entries WHERE parent_id=?", root)
	parent := root
	for i := 1; i <= folderMaxDepth; i++ {
		parent = testFolder(t, s, c["alice"], "personal", parent, fmt.Sprintf("level%d", i))
	}
	expectStatus(t, call(t, s, c["alice"], "POST", endpoint, nil), 400)
	// A revoked department membership must invalidate a prepared ZIP.
	dept := testFolder(t, s, c["alice"], "department", "", "部门资料")
	url = prepareTestArchive(t, s, c["alice"], dept)
	s.DB.Exec("UPDATE users SET dept_id='finance' WHERE id='alice'")
	expectStatus(t, call(t, s, c["alice"], "GET", url, nil), 404)
	s.DB.Exec("UPDATE entries SET name='C:unsafe' WHERE id=?", dept)
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/folders/"+dept+"/download", nil), 400)
	s.DB.Exec("UPDATE entries SET name='部门资料' WHERE id=?", dept)
	available, capacity, e := diskSpace(s.Config.DataDir)
	if e != nil || capacity == 0 {
		t.Fatal("disk space unavailable")
	}
	s.site.StorageWarningPercent = int(float64(available)/float64(capacity)*100) + 1
	expectStatus(t, call(t, s, c["maint"], "POST", "/api/folders/"+dept+"/download", nil), 507)
}
func TestIntegrationImagePreviewAndDownload(t *testing.T) {
	s, c := testServer(t)
	var encoded bytes.Buffer
	if e := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 16, 16))); e != nil {
		t.Fatal(e)
	}
	imageData := encoded.Bytes()
	w := call(t, s, c["alice"], "POST", "/api/uploads", map[string]any{"scope": "personal", "name": "image.png", "size": len(imageData)})
	expectStatus(t, w, 201)
	id := responseID(t, w.Body.Bytes())
	expectStatus(t, call(t, s, c["alice"], "PUT", "/api/uploads/"+id+"/chunks/0", imageData), 200)
	w = call(t, s, c["alice"], "POST", "/api/uploads/"+id+"/complete", nil)
	expectStatus(t, w, 201)
	file := responseID(t, w.Body.Bytes())
	s.scan = cleanTestScanner{}
	s.scanOne(context.Background(), file)
	for _, who := range []string{"alice", "admin", "maint"} {
		for _, suffix := range []string{"", "?preview=true"} {
			w = call(t, s, c[who], "GET", "/api/files/"+file+"/content"+suffix, nil)
			expectStatus(t, w, 200)
			disposition := "attachment"
			if suffix != "" {
				disposition = "inline"
			}
			if !strings.HasPrefix(w.Header().Get("Content-Disposition"), disposition) || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), imageData) {
				t.Fatal("wrong preview/download response")
			}
		}
	}
	expectStatus(t, call(t, s, c["bob"], "GET", "/api/files/"+file+"/content?preview=true", nil), 404)
	if inlineMIME("image/svg+xml") {
		t.Fatal("active SVG enabled inline")
	}
}
