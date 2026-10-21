package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type SiteSettings struct {
	CompanyName           string       `json:"company_name"`
	CompanyShortName      string       `json:"company_short_name"`
	Copyright             string       `json:"copyright"`
	CopyrightURL          string       `json:"copyright_url"`
	DefaultTheme          string       `json:"default_theme"`
	StorageRoot           string       `json:"storage_root"`
	StorageWarningPercent int          `json:"storage_warning_percent"`
	ListenPort            int          `json:"listen_port"`
	VirusScan             ScanSettings `json:"virus_scan"`
	DataDir               string       `json:"data_dir,omitempty"` // Read old settings during an offline upgrade only.
	LegacyRoot            string       `json:"legacy_root,omitempty"`
	Revision              int64        `json:"revision"`
}

func loadSiteSettings(db *sql.DB, c Config) (SiteSettings, []byte, error) {
	name := c.CompanyName
	if name == "" {
		name = "公司资源库"
	}
	v := SiteSettings{CompanyName: name, CompanyShortName: name, Copyright: "长沙感梦科技有限公司 版权所有", CopyrightURL: "https://www.gmkj.cc/", DefaultTheme: "explorer", DataDir: c.DataDir, LegacyRoot: c.LegacyRoot, Revision: 1}
	if c.StorageRoot != "" {
		v.StorageRoot = c.StorageRoot
		v.DataDir = ""
		v.LegacyRoot = ""
	}
	if utf8.RuneCountInString(v.CompanyShortName) > 24 {
		v.CompanyShortName = string([]rune(v.CompanyShortName)[:24])
	}
	v.StorageWarningPercent = 10
	v.VirusScan = defaultScanSettings()
	_, port, _ := net.SplitHostPort(c.Listen)
	v.ListenPort, _ = strconv.Atoi(port)
	b, _ := json.Marshal(v)
	if _, e := db.Exec("INSERT IGNORE INTO site_settings(id,settings) VALUES(1,?)", b); e != nil {
		return v, nil, fmt.Errorf("请先执行 migrate 初始化站点配置表: %w", e)
	}
	var logo []byte
	if e := db.QueryRow("SELECT settings,logo,revision FROM site_settings WHERE id=1").Scan(&b, &logo, &v.Revision); e != nil {
		return v, nil, e
	}
	revision := v.Revision
	if e := json.Unmarshal(b, &v); e != nil {
		return v, nil, e
	}
	v.Revision = revision
	if e := validateSiteSettings(v); e != nil {
		return v, nil, e
	}
	// Storage paths are immutable during a running process. Pending paths apply on restart.
	return v, logo, nil
}

func validateSiteSettings(v SiteSettings) error {
	if err := validateScanSettings(v.VirusScan); err != nil {
		return err
	}
	if v.ListenPort < 0 || v.ListenPort > 65535 {
		return bad("监听端口需为 1～65535")
	}
	if v.StorageWarningPercent < 1 || v.StorageWarningPercent > 99 {
		return bad("剩余空间告警阈值需为 1～99%")
	}
	for _, f := range []struct {
		value, label string
		max          int
	}{{v.CompanyName, "公司名称", 100}, {v.CompanyShortName, "公司简称", 24}, {v.Copyright, "底部版权", 200}} {
		if strings.TrimSpace(f.value) == "" || strings.TrimSpace(f.value) != f.value || utf8.RuneCountInString(f.value) > f.max || strings.ContainsAny(f.value, "\x00\r\n") {
			return bad(f.label + "不能为空、不能含换行或超出长度限制")
		}
	}
	if v.DefaultTheme != "explorer" && v.DefaultTheme != "library" && v.DefaultTheme != "classic" {
		return bad("请选择有效主题")
	}
	u, e := url.Parse(v.CopyrightURL)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || len(v.CopyrightURL) > 500 || strings.ContainsAny(v.CopyrightURL, "\x00\r\n") {
		return bad("版权链接必须是完整的 HTTP 或 HTTPS 网址")
	}
	if v.StorageRoot != "" {
		if !filepath.IsAbs(v.StorageRoot) || strings.ContainsAny(v.StorageRoot, "\x00\r\n") {
			return bad("文件存储和读取目录必须是绝对路径")
		}
		return nil
	}
	if !filepath.IsAbs(v.DataDir) || strings.ContainsAny(v.DataDir, "\x00\r\n") || (v.LegacyRoot != "" && (!filepath.IsAbs(v.LegacyRoot) || strings.ContainsAny(v.LegacyRoot, "\x00\r\n"))) {
		return bad("存储和读取目录必须填写部署电脑上的绝对路径")
	}
	return nil
}

func sameDirectory(a, b string) bool {
	return sourceHash(filepath.Clean(a), "") == sourceHash(filepath.Clean(b), "")
}

func (s *Server) validateSitePaths(v SiteSettings) error {
	if v.StorageRoot != "" {
		return validateStorageRoot(v.StorageRoot, s.Config)
	}
	data, e := canonicalRoot(v.DataDir)
	if e != nil {
		return bad("存储目录不存在或无法访问，请先在部署电脑创建目录")
	}
	if filepath.Dir(data) == data {
		return bad("存储目录不能是磁盘根目录")
	}
	log, e := canonicalRoot(s.Config.LogDir)
	if e != nil {
		return bad("日志目录不可用")
	}
	if within(data, log) || within(log, data) {
		return bad("存储目录不能与日志目录重叠")
	}
	if s.Config.ConfigPath != "" && within(data, s.Config.ConfigPath) {
		return bad("存储目录不能包含系统配置文件")
	}
	if v.LegacyRoot != "" {
		legacy, e := canonicalRoot(v.LegacyRoot)
		if e != nil {
			return bad("读取目录不存在或无法读取")
		}
		if filepath.Dir(legacy) == legacy {
			return bad("读取目录不能是磁盘根目录")
		}
		if within(data, legacy) || within(legacy, data) || within(log, legacy) || within(legacy, log) {
			return bad("读取目录不能与存储或日志目录重叠")
		}
		if s.Config.ConfigPath != "" && within(legacy, s.Config.ConfigPath) {
			return bad("读取目录不能包含系统配置文件")
		}
		f, e := os.Open(legacy)
		if e != nil {
			return bad("服务账号没有读取目录权限")
		}
		_, e = f.Readdirnames(1)
		f.Close()
		if e != nil && e != io.EOF {
			return bad("服务账号无法读取目录内容")
		}
	}
	f, e := os.CreateTemp(data, ".gmpan-write-check-*")
	if e != nil {
		return bad("服务账号没有存储目录写入权限")
	}
	name := f.Name()
	f.Close()
	if e = os.Remove(name); e != nil {
		return bad("服务账号没有存储目录删除临时文件权限")
	}
	return nil
}

func (s *Server) publicConfig(w http.ResponseWriter, _ *http.Request, _ *User) error {
	s.siteMu.RLock()
	defer s.siteMu.RUnlock()
	logo := ""
	if len(s.siteLogo) > 0 {
		logo = fmt.Sprintf("/api/site-logo?v=%d", s.site.Revision)
	}
	writeJSON(w, 200, map[string]any{"company_name": s.site.CompanyName, "company_short_name": s.site.CompanyShortName, "copyright": s.site.Copyright, "copyright_url": s.site.CopyrightURL, "default_theme": s.site.DefaultTheme, "logo_url": logo, "sms_enabled": s.smsEnabled(), "folder_transfer_limits": folderTransferLimits(), "virus_scan_mode": s.site.VirusScan.Mode})
	return nil
}
func (s *Server) siteLogoContent(w http.ResponseWriter, _ *http.Request, _ *User) error {
	s.siteMu.RLock()
	defer s.siteMu.RUnlock()
	if len(s.siteLogo) == 0 {
		return notfound()
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(s.siteLogo)
	return nil
}
func (s *Server) siteResponse() map[string]any {
	settings := s.site
	settings.DataDir = ""
	settings.LegacyRoot = ""
	_, activePort, _ := net.SplitHostPort(s.Config.Listen)
	currentPort, _ := strconv.Atoi(activePort)
	s.restartMu.RLock()
	canRestart := s.restartFn != nil
	s.restartMu.RUnlock()
	scheme := "http"
	if s.Config.TLSCert != "" {
		scheme = "https"
	}
	return map[string]any{"settings": settings, "has_logo": len(s.siteLogo) > 0, "active_storage_root": s.Config.StorageRoot, "deployment_dir": deploymentDirectory(), "migration_required": s.Config.StorageRoot == "", "previous_data_dir": s.Config.DataDir, "previous_source_dir": s.Config.LegacyRoot, "restart_required": !sameDirectory(s.site.StorageRoot, s.Config.StorageRoot) || (s.site.ListenPort != 0 && s.site.ListenPort != currentPort), "active_listen": s.Config.Listen, "active_port": currentPort, "active_scheme": scheme, "can_restart": canRestart, "instance_id": s.instanceID}
}
func (s *Server) getSiteSettings(w http.ResponseWriter, _ *http.Request, u *User) error {
	if e := superAdmin(u); e != nil {
		return e
	}
	s.siteMu.RLock()
	defer s.siteMu.RUnlock()
	writeJSON(w, 200, s.siteResponse())
	return nil
}
func (s *Server) saveSiteSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := superAdmin(u); e != nil {
		return e
	}
	var v SiteSettings
	if e := decode(w, r, &v); e != nil {
		return e
	}
	if v.DataDir != "" || v.LegacyRoot != "" {
		return bad("目录配置已升级，请刷新管理页面后重试")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isRestarting() {
		return conflict("站点正在重启，请稍后重试")
	}
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if v.Revision != s.site.Revision {
		return conflict("站点配置已被更新，请重新加载后保存")
	}
	if v.ListenPort != 0 && v.ListenPort != s.site.ListenPort {
		return bad("请使用“修改监听端口”按钮变更端口")
	}
	v.ListenPort = s.site.ListenPort
	v.VirusScan = s.site.VirusScan
	if v.StorageRoot == "" {
		if s.site.StorageRoot != "" {
			return bad("文件存储和读取目录不能为空")
		}
		v.DataDir = s.site.DataDir
		v.LegacyRoot = s.site.LegacyRoot
	} else {
		v.StorageRoot = filepath.Clean(v.StorageRoot)
	}
	if e := validateSiteSettings(v); e != nil {
		return e
	}
	if !sameDirectory(v.StorageRoot, s.Config.StorageRoot) {
		if e := s.validateSitePaths(v); e != nil {
			return e
		}
		var n int
		if e := s.DB.QueryRow("SELECT (SELECT COUNT(*) FROM entries WHERE storage_kind IN ('local','legacy'))+(SELECT COUNT(*) FROM uploads WHERE status='uploading')+(SELECT COUNT(*) FROM import_jobs WHERE status='running')").Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return conflict("已有文件、索引或未完成任务，不能直接切换目录；请备份、停服后执行 unify-storage 迁移")
		}
		if e := checkStorageWritable(v.StorageRoot); e != nil {
			return e
		}
	}
	v.Revision++
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if _, e = s.DB.Exec("UPDATE site_settings SET settings=?,revision=? WHERE id=1", b, v.Revision); e != nil {
		return e
	}
	s.site = v
	s.audit(u.ID, "site_settings_update", "site", "ok")
	writeJSON(w, 200, s.siteResponse())
	return nil
}
func (s *Server) uploadSiteLogo(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := superAdmin(u); e != nil {
		return e
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return bad("Logo 文件不能超过 2 MB")
	}
	info, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || (format != "png" && format != "jpeg") || info.Width < 1 || info.Height < 1 || info.Width > 1024 || info.Height > 1024 {
		return bad("Logo 仅支持 PNG/JPEG，宽高均不超过 1024 像素")
	}
	im, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return bad("Logo 图片无法解码")
	}
	var out bytes.Buffer
	if e = png.Encode(&out, im); e != nil {
		return e
	}
	if out.Len() > 2<<20 {
		return bad("处理后的 Logo 超过 2 MB，请缩小图片")
	}
	return s.storeLogo(w, u, out.Bytes())
}
func (s *Server) deleteSiteLogo(w http.ResponseWriter, _ *http.Request, u *User) error {
	if e := superAdmin(u); e != nil {
		return e
	}
	return s.storeLogo(w, u, nil)
}
func (s *Server) storeLogo(w http.ResponseWriter, u *User, b []byte) error {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if _, e := s.DB.Exec("UPDATE site_settings SET logo=?,revision=revision+1 WHERE id=1", b); e != nil {
		return e
	}
	s.siteLogo = b
	s.site.Revision++
	s.audit(u.ID, "site_logo_update", "site", "ok")
	writeJSON(w, 200, s.siteResponse())
	return nil
}
func (s *Server) directoryChangePending(_ bool) bool {
	s.siteMu.RLock()
	defer s.siteMu.RUnlock()
	return !sameDirectory(s.site.StorageRoot, s.Config.StorageRoot)
}
