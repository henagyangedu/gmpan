package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationImagePreviewWithoutVirusCheck(t *testing.T) {
	s, clients := testServer(t)
	picture := image.NewRGBA(image.Rect(0, 0, 16, 12))
	picture.Set(4, 4, color.RGBA{R: 80, G: 160, B: 120, A: 255})
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var original bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&original, picture)
			} else {
				err = jpeg.Encode(&original, picture, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			name := "待检查图片." + format
			if err := os.WriteFile(filepath.Join(s.Config.LegacyRoot, name), original.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			runTestImport(t, s)
			var id string
			if err := s.DB.QueryRow("SELECT id FROM entries WHERE name=?", name).Scan(&id); err != nil {
				t.Fatal(err)
			}
			content := "/api/files/" + id + "/content"
			saveTestScanPolicy(t, s, clients["admin"], "immediate", "22:00", "06:00")
			expectStatus(t, call(t, s, clients["alice"], "GET", content+"?preview=true", nil), 423)
			for _, mode := range []string{"off", "scheduled"} {
				saveTestScanPolicy(t, s, clients["admin"], mode, "22:00", "06:00")
				for _, status := range []string{"pending", "scanning"} {
					if _, err := s.DB.Exec("UPDATE entries SET scan_status=? WHERE id=?", status, id); err != nil {
						t.Fatal(err)
					}
					detail := call(t, s, clients["alice"], "GET", "/api/files/"+id, nil)
					expectStatus(t, detail, 200)
					var entry Entry
					if err := json.Unmarshal(detail.Body.Bytes(), &entry); err != nil || !entry.CanDownload || !entry.ScanWarning || entry.MIME != "image/"+format {
						t.Fatalf("unchecked image must be accessible with warning: %s", detail.Body.String())
					}
					for _, preview := range []bool{true, false} {
						path, disposition := content, "attachment"
						if preview {
							path, disposition = path+"?preview=true", "inline"
						}
						response := call(t, s, clients["alice"], "GET", path, nil)
						expectStatus(t, response, 200)
						if response.Header().Get("Content-Type") != entry.MIME || !strings.HasPrefix(response.Header().Get("Content-Disposition"), disposition) || !bytes.Equal(response.Body.Bytes(), original.Bytes()) {
							t.Fatalf("image content mismatch: mode=%s status=%s preview=%v", mode, status, preview)
						}
						if _, _, err := image.Decode(bytes.NewReader(response.Body.Bytes())); err != nil {
							t.Fatalf("preview image cannot be decoded: %v", err)
						}
					}
				}
				for _, status := range []string{"infected", "failed"} {
					if _, err := s.DB.Exec("UPDATE entries SET scan_status=? WHERE id=?", status, id); err != nil {
						t.Fatal(err)
					}
					expectStatus(t, call(t, s, clients["alice"], "GET", content+"?preview=true", nil), 423)
				}
			}
			expectStatus(t, call(t, s, testClient{}, "GET", content+"?preview=true", nil), 401)
		})
	}
}
