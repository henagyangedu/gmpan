package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanWindow(t *testing.T) {
	for _, test := range []struct {
		mode, start, end, now string
		active                bool
	}{
		{"off", "22:00", "06:00", "2026-10-20T23:00:00+08:00", false},
		{"immediate", "22:00", "06:00", "2026-10-20T12:00:00+08:00", true},
		{"scheduled", "22:00", "06:00", "2026-10-20T21:59:59+08:00", false},
		{"scheduled", "22:00", "06:00", "2026-10-20T22:00:00+08:00", true},
		{"scheduled", "22:00", "06:00", "2026-10-21T05:59:59+08:00", true},
		{"scheduled", "22:00", "06:00", "2026-10-21T06:00:00+08:00", false},
		{"scheduled", "09:00", "18:00", "2026-10-20T09:00:00+08:00", true},
		{"scheduled", "09:00", "18:00", "2026-10-20T18:00:00+08:00", false},
		{"scheduled", "22:00", "06:00", "2026-10-20T14:00:00Z", true},
	} {
		now, _ := time.Parse(time.RFC3339, test.now)
		v := ScanSettings{test.mode, test.start, test.end}
		active, end := v.window(now)
		if active != test.active || (active && !end.IsZero() && !end.After(now)) {
			t.Fatalf("bad window %v active=%v end=%v", test, active, end)
		}
	}
	for _, v := range []ScanSettings{{"unknown", "22:00", "06:00"}, {"scheduled", "9:00", "06:00"}, {"scheduled", "22:00", "22:00"}} {
		if validateScanSettings(v) == nil {
			t.Fatalf("invalid policy accepted: %v", v)
		}
	}
}

func saveTestScanPolicy(t *testing.T, s *Server, c testClient, mode, start, end string) {
	t.Helper()
	response := call(t, s, c, "PUT", "/api/admin/scan-settings", map[string]any{"mode": mode, "start": start, "end": end, "revision": s.site.Revision})
	expectStatus(t, response, 200)
}

func TestIntegrationScanPolicyAccessAndScheduling(t *testing.T) {
	s, c := testServer(t)
	s.scan = cleanTestScanner{}
	for _, user := range []string{"maint", "alice"} {
		expectStatus(t, call(t, s, c[user], "GET", "/api/admin/scan-settings", nil), 403)
		expectStatus(t, call(t, s, c[user], "PUT", "/api/admin/scan-settings", map[string]any{"mode": "off", "start": "22:00", "end": "06:00", "revision": s.site.Revision}), 403)
	}
	expectStatus(t, call(t, s, c["admin"], "PUT", "/api/admin/scan-settings", map[string]any{"mode": "off", "start": "22:00", "end": "06:00", "revision": 0}), 409)
	root := testFolder(t, s, c["alice"], "personal", "", "未检查目录")
	id := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal", "parent_id": root})
	content := "/api/files/" + id + "/content"
	expectStatus(t, call(t, s, c["alice"], "GET", content, nil), 423)
	saveTestScanPolicy(t, s, c["admin"], "off", "22:00", "06:00")
	expectStatus(t, call(t, s, c["alice"], "GET", content+"?preview=true", nil), 200)
	expectStatus(t, call(t, s, c["bob"], "GET", content, nil), 404)
	var entry Entry
	if err := json.Unmarshal(call(t, s, c["alice"], "GET", "/api/files/"+id, nil).Body.Bytes(), &entry); err != nil || !entry.CanDownload || !entry.ScanWarning || entry.ScanStatus != "pending" {
		t.Fatalf("unscanned file must remain marked: %+v %v", entry, err)
	}
	s.scanBatch(context.Background())
	var status string
	s.DB.QueryRow("SELECT scan_status FROM entries WHERE id=?", id).Scan(&status)
	if status != "pending" {
		t.Fatal("off mode unexpectedly scanned a file")
	}
	zipURL := prepareTestArchive(t, s, c["alice"], root)
	// A switch back to strict mode also invalidates already prepared downloads.
	saveTestScanPolicy(t, s, c["admin"], "immediate", "22:00", "06:00")
	expectStatus(t, call(t, s, c["alice"], "GET", zipURL, nil), 409)
	saveTestScanPolicy(t, s, c["admin"], "scheduled", "22:00", "06:00")
	zipURL = prepareTestArchive(t, s, c["alice"], root)
	expectStatus(t, call(t, s, c["alice"], "GET", zipURL, nil), 200)
	batch := call(t, s, c["alice"], "POST", "/api/files/batch-download", map[string]any{"ids": []string{id}})
	expectStatus(t, batch, 201)
	var job struct {
		URL string `json:"url"`
	}
	json.Unmarshal(batch.Body.Bytes(), &job)
	expectStatus(t, call(t, s, c["alice"], "GET", job.URL, nil), 200)
	for _, blocked := range []string{"infected", "failed"} {
		s.DB.Exec("UPDATE entries SET scan_status=? WHERE id=?", blocked, id)
		for _, mode := range []string{"off", "scheduled"} {
			saveTestScanPolicy(t, s, c["admin"], mode, "22:00", "06:00")
			expectStatus(t, call(t, s, c["alice"], "GET", content, nil), 423)
			expectStatus(t, call(t, s, c["alice"], "POST", "/api/folders/"+root+"/download", nil), 423)
		}
	}
	// Imported image types are available before any virus check, and a second
	// directory scan preserves the pending status instead of claiming clean.
	image := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(s.Config.LegacyRoot, "未检查.png"), image, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		jobID := newID()
		s.DB.Exec("INSERT INTO import_jobs(id,root_hash,user_id) VALUES(?,?,?)", jobID, sourceHash(s.Config.LegacyRoot, ""), "admin")
		s.runImport(context.Background(), jobID, s.Config.LegacyRoot, "admin")
	}
	response := call(t, s, c["alice"], "GET", "/api/files?scope=company&type=image&q=未检查", nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var result struct {
		Items []Entry `json:"items"`
	}
	json.Unmarshal(response.Body.Bytes(), &result)
	if len(result.Items) != 1 || result.Items[0].MIME != "image/png" || !result.Items[0].CanDownload || !result.Items[0].ScanWarning {
		t.Fatalf("pending image classification missing: %s", response.Body.String())
	}
}

type pausingTestScanner struct{ started chan struct{} }

func (p pausingTestScanner) Check(ctx context.Context, path string) (string, string) {
	close(p.started)
	<-ctx.Done()
	return "failed", "cancelled"
}
func TestIntegrationScanPolicyPausesCurrentWork(t *testing.T) {
	s, c := testServer(t)
	scanner := pausingTestScanner{make(chan struct{})}
	s.scan = scanner
	id := uploadTestFile(t, s, c["alice"], map[string]any{"scope": "personal"})
	done := make(chan struct{})
	go func() { s.scanOne(context.Background(), id); close(done) }()
	select {
	case <-scanner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("scanner never started")
	}
	saveTestScanPolicy(t, s, c["admin"], "off", "22:00", "06:00")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("policy change did not pause active scan")
	}
	var status string
	s.DB.QueryRow("SELECT scan_status FROM entries WHERE id=?", id).Scan(&status)
	if status != "pending" {
		t.Fatal("interrupted check must return to pending, got", status)
	}
}
