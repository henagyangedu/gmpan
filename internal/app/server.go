package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gmpan/internal/webui"
)

type Server struct {
	DB            *sql.DB
	Config        Config
	Log           *slog.Logger
	mux           *http.ServeMux
	mu            sync.Mutex
	scan          Scanner
	smsClient     *http.Client
	siteMu        sync.RWMutex
	site          SiteSettings
	siteLogo      []byte
	smsMu         sync.RWMutex
	smsSettings   SMSSettings
	archiveMu     sync.Mutex
	archiveBusy   bool
	archiveJob    *folderArchive
	restartMu     sync.RWMutex
	restartFn     func()
	restarting    bool
	instanceID    string
	workers       sync.WaitGroup
	scanControlMu sync.Mutex
	scanWorkMu    sync.Mutex
	scanCancel    context.CancelFunc
}
type apiError struct {
	Status  int
	Message string
}

func (e apiError) Error() string { return e.Message }
func bad(s string) error         { return apiError{400, s} }
func forbidden() error           { return apiError{403, "无权访问此资源"} }
func notfound() error            { return apiError{404, "资源不存在或不可访问"} }
func conflict(s string) error    { return apiError{409, s} }

type endpoint func(http.ResponseWriter, *http.Request, *User) error

func NewServer(db *sql.DB, c Config, logger *slog.Logger) (*Server, error) {
	settings, logo, err := loadSiteSettings(db, c)
	if err != nil {
		return nil, err
	}
	if settings.StorageRoot != "" {
		if err := prepareStorageRoot(settings.StorageRoot, c); err != nil {
			return nil, err
		}
		c = unifiedConfig(c, settings.StorageRoot)
	} else {
		c.DataDir, c.LegacyRoot = settings.DataDir, settings.LegacyRoot
		c.StorageRoot = ""
	}
	for _, d := range []string{c.DataDir, c.LogDir, filepath.Join(c.DataDir, "objects"), filepath.Join(c.DataDir, "chunks")} {
		if e := os.MkdirAll(d, 0700); e != nil {
			return nil, e
		}
	}
	if settings.ListenPort > 0 {
		host, _, err := net.SplitHostPort(c.Listen)
		if err != nil {
			return nil, err
		}
		c.Listen = net.JoinHostPort(host, fmt.Sprint(settings.ListenPort))
	}
	s := &Server{DB: db, Config: c, Log: logger, mux: http.NewServeMux(), instanceID: newID(), smsClient: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.site, s.siteLogo = settings, logo
	if err := s.validateSitePaths(settings); err != nil {
		return nil, err
	}
	if s.smsSettings, err = loadSMSSettings(db); err != nil {
		return nil, err
	}
	if c.ClamScanPath != "" {
		s.scan = CommandScanner{Path: c.ClamScanPath, DatabaseDir: c.ClamDatabaseDir}
	}
	s.routes()
	s.cleanupFolderDownloads()
	return s, nil
}
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; media-src 'self' blob:; frame-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'")
		defer func() {
			if recover() != nil {
				s.Log.Error("request panic", "method", r.Method)
				http.Error(w, "服务暂时不可用", 500)
			}
		}()
		s.mux.ServeHTTP(w, r)
	})
}
func (s *Server) route(pattern string, authed bool, fn endpoint) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var u *User
		var e error
		if r.Method != "GET" && r.Method != "HEAD" {
			if origin := r.Header.Get("Origin"); origin != "" {
				o, err := url.Parse(origin)
				if err != nil || o.Host != r.Host || (o.Scheme != "http" && o.Scheme != "https") {
					s.replyError(w, forbidden())
					return
				}
			}
		}
		if authed {
			u, e = s.authenticate(r)
			if e == nil && r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-CSRF-Token") != u.CSRF {
				e = forbidden()
			}
		}
		if e == nil {
			if r.Method != "GET" && r.Method != "HEAD" && s.isRestarting() {
				s.replyError(w, apiError{503, "站点正在重启，请稍后重试"})
				return
			}
			e = fn(w, r, u)
		}
		if e != nil {
			s.replyError(w, e)
		}
	})
}
func (s *Server) replyError(w http.ResponseWriter, e error) {
	var a apiError
	if errors.As(e, &a) {
		writeJSON(w, a.Status, map[string]any{"error": a.Message})
		return
	}
	s.Log.Error("operation failed", "error", e.Error())
	writeJSON(w, 500, map[string]any{"error": "操作失败，请查看系统日志或联系管理员"})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return bad("请求内容格式不正确")
	}
	if d.Decode(new(any)) != io.EOF {
		return bad("请求内容格式不正确")
	}
	return nil
}
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func (s *Server) audit(u, action, target, result string) {
	if _, e := s.DB.Exec("INSERT INTO audit_logs(user_id,action,target_id,result) VALUES(?,?,?,?)", u, action, target, result); e != nil {
		s.Log.Error("audit write failed")
	}
	s.Log.Info("audit", "user_id", u, "action", action, "target_id", target, "result", result)
}
func (s *Server) routes() {
	s.route("GET /api/health", false, func(w http.ResponseWriter, r *http.Request, _ *User) error {
		ctx, c := context.WithTimeout(r.Context(), 2*time.Second)
		defer c()
		if s.DB.PingContext(ctx) != nil {
			return apiError{503, "数据库不可用"}
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "instance_id": s.instanceID})
		return nil
	})
	s.route("GET /api/public-config", false, s.publicConfig)
	s.route("GET /api/site-logo", false, s.siteLogoContent)
	s.route("GET /api/admin/site-settings", true, s.getSiteSettings)
	s.route("GET /api/admin/sms-settings", true, s.getSMSSettings)
	s.route("PUT /api/admin/sms-settings", true, s.saveSMSSettings)
	s.route("POST /api/admin/sms-test", true, s.testSMSSettings)
	s.route("PUT /api/admin/site-settings", true, s.saveSiteSettings)
	s.route("GET /api/admin/scan-settings", true, s.getScanSettings)
	s.route("PUT /api/admin/scan-settings", true, s.saveScanSettings)
	s.route("GET /api/admin/directories", true, s.browseDirectories)
	s.route("PUT /api/admin/listen-port", true, s.saveListenPort)
	s.route("POST /api/admin/restart", true, s.restartSite)
	s.route("PUT /api/admin/site-logo", true, s.uploadSiteLogo)
	s.route("DELETE /api/admin/site-logo", true, s.deleteSiteLogo)
	s.route("GET /api/company/folders", true, s.companyFolders)
	s.route("POST /api/auth/login", false, s.login)
	s.route("POST /api/auth/sms/send", false, s.sendSMS)
	s.route("POST /api/auth/sms/login", false, s.smsLogin)
	s.route("GET /api/me", true, func(w http.ResponseWriter, _ *http.Request, u *User) error { writeJSON(w, 200, u); return nil })
	s.route("POST /api/auth/logout", true, s.logout)
	s.route("POST /api/auth/password", true, s.changePassword)
	s.route("GET /api/files", true, s.listFiles)
	s.route("POST /api/files/batch-trash", true, s.batchTrashFiles)
	s.route("POST /api/files/batch-download", true, s.prepareBatchDownload)
	s.route("GET /api/files/{id}", true, s.fileDetail)
	s.route("GET /api/files/{id}/content", true, s.content)
	s.route("POST /api/folders", true, s.createFolder)
	s.route("POST /api/folders/{id}/download", true, s.prepareFolderDownload)
	s.route("GET /api/folder-downloads/{token}", true, s.downloadFolderArchive)
	s.route("PATCH /api/files/{id}", true, s.editFile)
	s.route("POST /api/files/{id}/trash", true, s.trashFile)
	s.route("POST /api/files/{id}/restore", true, s.restoreFile)
	s.route("POST /api/files/{id}/approve", true, s.approveFile)
	s.route("GET /api/stats", true, s.stats)
	s.route("GET /api/storage-status", true, s.storageStatus)
	s.route("GET /api/uploads", true, s.listUploads)
	s.route("POST /api/uploads", true, s.initUpload)
	s.route("GET /api/uploads/{id}", true, s.uploadStatus)
	s.route("PUT /api/uploads/{id}/chunks/{index}", true, s.putChunk)
	s.route("POST /api/uploads/{id}/complete", true, s.completeUpload)
	s.route("DELETE /api/uploads/{id}", true, s.cancelUpload)
	s.route("GET /api/departments", true, s.departments)
	s.route("POST /api/admin/departments", true, s.createDepartment)
	s.route("GET /api/admin/users", true, s.users)
	s.route("POST /api/admin/users", true, s.createUser)
	s.route("PATCH /api/admin/users/{id}", true, s.editUser)
	s.route("GET /api/admin/system", true, s.systemInfo)
	s.route("GET /api/admin/audit", true, s.auditList)
	s.route("GET /api/admin/imports", true, s.imports)
	s.route("POST /api/admin/imports", true, s.startImport)
	s.route("POST /api/admin/scans/retry", true, s.retryScans)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "接口不存在"})
	})
	assets, _ := fs.Sub(webui.Files, "dist")
	fileServer := http.FileServer(http.FS(assets))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if _, e := fs.Stat(assets, strings.TrimPrefix(r.URL.Path, "/")); e != nil {
			b, e := fs.ReadFile(assets, "index.html")
			if e != nil {
				http.Error(w, "请先构建前端", 503)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(b)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
func ipKey(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	return digest(host)
}
func (s *Server) limited(subject, ip, purpose string, maxSubject, maxIP int) (bool, error) {
	var n, m int
	e := s.DB.QueryRow("SELECT COALESCE(SUM(subject_hash=?),0),COALESCE(SUM(ip_hash=?),0) FROM auth_attempts WHERE purpose=? AND created_at>DATE_SUB(NOW(),INTERVAL 10 MINUTE) AND (subject_hash=? OR ip_hash=?)", digest(subject), ip, purpose, digest(subject), ip).Scan(&n, &m)
	return n >= maxSubject || m >= maxIP, e
}
func (s *Server) attempt(subject, ip, purpose string) {
	_, _ = s.DB.Exec("INSERT INTO auth_attempts(subject_hash,ip_hash,purpose) VALUES(?,?,?)", digest(subject), ip, purpose)
}
