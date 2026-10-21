package app

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func (s *Server) SetRestartHandler(fn func()) {
	s.restartMu.Lock()
	s.restartFn = fn
	s.restartMu.Unlock()
}
func (s *Server) isRestarting() bool {
	s.restartMu.RLock()
	defer s.restartMu.RUnlock()
	return s.restarting
}

func (s *Server) checkPort(port int) error {
	if port < 1 || port > 65535 {
		return bad("监听端口需为 1～65535")
	}
	host, active, err := net.SplitHostPort(s.Config.Listen)
	if err != nil {
		return bad("当前监听地址不可用，请检查部署配置")
	}
	if active == strconv.Itoa(port) {
		return nil
	}
	if err := CheckListenAddress(net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
		return conflict("该端口已被占用或无法监听，请选择其他端口")
	}
	return nil
}

func (s *Server) saveListenPort(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := superAdmin(u); err != nil {
		return err
	}
	var p struct {
		Port     int   `json:"port"`
		Revision int64 `json:"revision"`
	}
	if err := decode(w, r, &p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if s.isRestarting() {
		return conflict("站点正在重启，请稍后重试")
	}
	if p.Revision != s.site.Revision {
		return conflict("站点配置已更新，请重新加载后再修改端口")
	}
	if err := s.checkPort(p.Port); err != nil {
		return err
	}
	v := s.site
	v.ListenPort = p.Port
	v.Revision++
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = s.DB.Exec("UPDATE site_settings SET settings=?,revision=? WHERE id=1", b, v.Revision); err != nil {
		return err
	}
	s.site = v
	s.audit(u.ID, "site_listen_port_update", strconv.Itoa(p.Port), "ok")
	writeJSON(w, 200, s.siteResponse())
	return nil
}

func checkStorageWritable(root string) error {
	target := root
	managed := filepath.Join(root, ".gmpan")
	if info, err := os.Stat(managed); err == nil && info.IsDir() {
		target = managed
	}
	for _, dir := range []string{target, filepath.Join(managed, "objects"), filepath.Join(managed, "chunks")} {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		f, err := os.CreateTemp(dir, ".gmpan-write-check-*")
		if err != nil {
			return bad("服务账号无法写入所选目录，请在服务器授予该账号相应目录权限后重试")
		}
		name := f.Name()
		f.Close()
		if err = os.Remove(name); err != nil {
			return bad("服务账号无法删除所选目录中的临时文件，请检查目录权限")
		}
	}
	return nil
}

func (s *Server) restartSite(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := superAdmin(u); err != nil {
		return err
	}
	var p struct {
		Revision int64 `json:"revision"`
	}
	if err := decode(w, r, &p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.siteMu.RLock()
	defer s.siteMu.RUnlock()
	if p.Revision != s.site.Revision {
		return conflict("站点配置已更新，请重新加载后再重启")
	}
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	if s.restartFn == nil {
		return conflict("当前运行方式未启用站点重启，请使用新版服务程序")
	}
	if s.restarting {
		return conflict("站点正在重启，请稍后重试")
	}
	var running int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM import_jobs WHERE status='running'").Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return conflict("资料扫描任务正在运行，请等待完成后再重启站点")
	}
	if s.site.StorageRoot != "" {
		if err := s.validateSitePaths(s.site); err != nil {
			return err
		}
		if err := checkStorageWritable(s.site.StorageRoot); err != nil {
			return err
		}
	}
	if s.site.ListenPort > 0 {
		if err := s.checkPort(s.site.ListenPort); err != nil {
			return err
		}
	}
	s.restarting = true
	s.audit(u.ID, "site_restart", "site", "accepted")
	fn := s.restartFn
	// Respond before shutting down the listener. No SCM privileges or machine
	// restart are needed; the HTTP site and workers restart inside the service.
	time.AfterFunc(700*time.Millisecond, fn)
	writeJSON(w, 202, map[string]any{"message": "站点正在重启，稍后将重新连接", "port": s.site.ListenPort, "instance_id": s.instanceID})
	return nil
}
