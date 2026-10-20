//go:build windows

package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallServiceIdentity(t *testing.T) {
	for _, test := range []struct {
		name, path string
		eligible   bool
	}{
		{appService, `"C:\GmPan\gmpan.exe" serve -config "C:\GmPan\config.json"`, true},
		{appService, `"C:\GmPan\other.exe" serve`, false},
		{appService, `"C:\GmPan\gmpan.exe" migrate`, false},
		{mysqlService, `"C:\GmPan\components\mysql-8.4.11-winx64\bin\mysqld.exe" --defaults-file=C:\GmPan\mysql.ini GmPanMySQL84`, true},
		{mysqlService, `"C:\OtherDB\bin\mysqld.exe"`, false},
		{"MySQL", `"C:\GmPan\components\mysql-8.4.11-winx64\bin\mysqld.exe"`, false},
		{appService, `gmpan.exe serve`, false},
	} {
		item, err := identifyUninstallService(test.name, test.path)
		if (err == nil && item.Eligible) != test.eligible {
			t.Fatalf("identity check mismatch: %+v error=%v", test, err)
		}
	}
}
func TestUninstallRequiresExplicitSelectionAndConfirmation(t *testing.T) {
	w := New(t.TempDir())
	w.Host = "127.0.0.1:18282"
	for _, body := range []string{`{}`, `{"confirmed":true,"services":[]}`, `{"confirmed":false,"services":[{"name":"GmPan","path":"missing"}]}`} {
		if out := setupRequest(w, "POST", "/api/setup/uninstall", []byte(body)); out.Code != 400 {
			t.Fatalf("unconfirmed uninstall accepted: %d", out.Code)
		}
	}
	w.checking = true
	if out := setupRequest(w, "POST", "/api/setup/uninstall", []byte(`{"confirmed":true,"services":[{"name":"GmPan","path":"missing"}]}`)); out.Code != 409 {
		t.Fatal("uninstall accepted while check running")
	}
}
func checkInstalledUninstall(t *testing.T, bundle string, p Request) {
	t.Helper()
	services, err := inspectUninstallServices()
	if err != nil {
		t.Fatal(err)
	}
	request := uninstallRequest{Confirmed: true}
	for _, service := range services {
		if service.Directory != p.Directory || !service.Eligible {
			t.Fatal("refuse to uninstall a service outside this test installation", service.Name)
		}
		request.Services = append(request.Services, struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}{service.Name, service.Path})
	}
	if len(request.Services) != 2 {
		t.Fatal("test installation services not found")
	}
	marker := filepath.Join(p.Storage, "卸载保留测试.txt")
	if err = os.WriteFile(marker, []byte("keep original files"), 0600); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(p.Directory, "config.json"), filepath.Join(p.Directory, "gmpan.exe"), filepath.Join(p.Directory, "mysql-data", "auto.cnf"), marker}
	before := map[string][]byte{}
	for _, file := range files {
		before[file], err = os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
	}
	w := New(bundle)
	w.state = Progress{Running: true, Operation: "uninstall", Steps: []string{}}
	w.uninstall(request)
	if !w.state.Done || w.state.Error != "" {
		t.Fatalf("uninstall failed: %s steps=%v", w.state.Error, w.state.Steps)
	}
	remaining, err := inspectUninstallServices()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("services were not removed: %v %v", remaining, err)
	}
	for _, file := range files {
		after, e := os.ReadFile(file)
		if e != nil || !bytes.Equal(after, before[file]) {
			t.Fatal("uninstall modified retained data", file, e)
		}
	}
	if _, err = os.Stat(filepath.Join(p.Storage, ".gmpan", "objects")); err != nil {
		t.Fatal("uploaded file directory was not preserved", err)
	}
	if _, err = os.Stat(w.state.Log); err != nil {
		t.Fatal("uninstall log missing", err)
	}
	t.Log("GUI-uninstaller backend removed only the isolated test services, with database identity/config/program/original files/upload directory preserved")
}

func TestIntegrationUninstallOrphanRecords(t *testing.T) {
	base := os.Getenv("GMPAN_SETUP_TEST_ORPHAN_DIRECTORY")
	if base == "" {
		t.Skip("set an explicit isolated orphan-service test directory")
	}
	base, err := filepath.Abs(base)
	if err != nil || !strings.Contains(strings.ToLower(base), `\.local\setup-test-`) {
		t.Fatal("orphan test path must be an isolated .local/setup-test-* directory")
	}
	if _, err = os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("orphan test directory must not exist")
	}
	if err = serviceAbsent(appService); err != nil {
		t.Fatal(err)
	}
	if err = serviceAbsent(mysqlService); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, name := range []string{appService, mysqlService} {
			if err := removeTestService(name, base); err != nil {
				t.Error(err)
			}
		}
	}()
	for _, item := range []struct {
		name, exe string
		args      []string
	}{
		{appService, filepath.Join(base, "gmpan.exe"), []string{"serve", "-config", filepath.Join(base, "config.json")}},
		{mysqlService, filepath.Join(base, "components", "mysql-8.4.11-winx64", "bin", "mysqld.exe"), []string{"--defaults-file=" + filepath.Join(base, "mysql.ini"), mysqlService}},
	} {
		s, err := createService(item.name, item.exe, item.args, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	items, err := inspectUninstallServices()
	if err != nil {
		t.Fatal(err)
	}
	p := uninstallRequest{Confirmed: true}
	for _, item := range items {
		if !item.Eligible || item.Exists || item.Directory != base {
			t.Fatalf("missing-program service was not correctly detected: %+v", item)
		}
		p.Services = append(p.Services, struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}{item.Name, item.Path})
	}
	if len(p.Services) != 2 {
		t.Fatal("expected the two isolated orphan records")
	}
	w := New(t.TempDir())
	w.state = Progress{Running: true, Operation: "uninstall", Steps: []string{}}
	w.uninstall(p)
	if !w.state.Done || w.state.Error != "" {
		t.Fatal("orphan cleanup failed", w.state.Error, w.state.Steps)
	}
	if err = serviceAbsent(appService); err != nil {
		t.Fatal(err)
	}
	if err = serviceAbsent(mysqlService); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("uninstall unexpectedly created or changed the deleted deployment directory")
	}
	t.Log("missing-program GmPan and dedicated MySQL service records removed without creating or deleting deployment/data files")
}
