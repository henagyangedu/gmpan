package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type ScanSettings struct {
	Mode  string `json:"mode"`
	Start string `json:"start"`
	End   string `json:"end"`
}

var scanLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func defaultScanSettings() ScanSettings { return ScanSettings{"immediate", "22:00", "06:00"} }
func validateScanSettings(v ScanSettings) error {
	if v == (ScanSettings{}) {
		return nil
	} // Older clients/settings retain the original policy.
	if v.Mode != "off" && v.Mode != "immediate" && v.Mode != "scheduled" {
		return bad("请选择有效的病毒检查模式")
	}
	for _, value := range []string{v.Start, v.End} {
		parsed, err := time.Parse("15:04", value)
		if err != nil || parsed.Format("15:04") != value {
			return bad("检查时间需为 HH:mm 格式")
		}
	}
	if v.Start == v.End {
		return bad("开始和结束时间不能相同；全天检查请选择立即检查")
	}
	return nil
}
func (s *Server) scanSettings() ScanSettings {
	s.siteMu.RLock()
	defer s.siteMu.RUnlock()
	if s.site.VirusScan == (ScanSettings{}) {
		return defaultScanSettings()
	}
	return s.site.VirusScan
}
func (v ScanSettings) window(now time.Time) (bool, time.Time) {
	if v.Mode == "off" {
		return false, time.Time{}
	}
	if v.Mode != "scheduled" {
		return true, time.Time{}
	}
	now = now.In(scanLocation)
	start, _ := time.Parse("15:04", v.Start)
	end, _ := time.Parse("15:04", v.End)
	a := time.Date(now.Year(), now.Month(), now.Day(), start.Hour(), start.Minute(), 0, 0, scanLocation)
	b := time.Date(now.Year(), now.Month(), now.Day(), end.Hour(), end.Minute(), 0, 0, scanLocation)
	if !b.After(a) {
		if now.Before(b) {
			a = a.AddDate(0, 0, -1)
		} else {
			b = b.AddDate(0, 0, 1)
		}
	}
	return !now.Before(a) && now.Before(b), b
}
func (s *Server) scanAccess(v *Entry) bool {
	if v.SourceMissing {
		return false
	}
	if v.ScanStatus == "clean" {
		return true
	}
	mode := s.scanSettings().Mode
	return (mode == "off" || mode == "scheduled") && (v.ScanStatus == "pending" || v.ScanStatus == "scanning")
}
func (s *Server) presentEntry(v *Entry) {
	v.CanDownload = v.Kind == "file" && !v.Deleted && s.scanAccess(v)
	v.ScanWarning = v.CanDownload && v.ScanStatus != "clean"
	if v.ScanWarning && v.MIME == "application/octet-stream" {
		if path, err := s.entryPath(v); err == nil {
			v.MIME = sniffFileMIME(path)
		}
	}
}
func (s *Server) scanSettingsResponse() map[string]any {
	s.siteMu.RLock()
	v, revision := s.site.VirusScan, s.site.Revision
	s.siteMu.RUnlock()
	if v == (ScanSettings{}) {
		v = defaultScanSettings()
	}
	active, _ := v.window(time.Now())
	return map[string]any{"settings": v, "revision": revision, "scanner_configured": s.scan != nil, "active_now": active && s.scan != nil, "timezone": "北京时间（UTC+8）"}
}
func (s *Server) getScanSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := superAdmin(u); err != nil {
		return err
	}
	writeJSON(w, 200, s.scanSettingsResponse())
	return nil
}
func (s *Server) saveScanSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := superAdmin(u); err != nil {
		return err
	}
	var p struct {
		ScanSettings
		Revision int64 `json:"revision"`
	}
	if err := decode(w, r, &p); err != nil {
		return err
	}
	if p.ScanSettings == (ScanSettings{}) {
		return bad("请选择病毒检查模式及时间")
	}
	if err := validateScanSettings(p.ScanSettings); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isRestarting() {
		return conflict("站点正在重启，请稍后重试")
	}
	s.siteMu.Lock()
	if p.Revision != s.site.Revision {
		s.siteMu.Unlock()
		return conflict("站点配置已更新，请重新加载后保存")
	}
	v := s.site
	v.VirusScan = p.ScanSettings
	v.Revision++
	b, err := json.Marshal(v)
	if err == nil {
		_, err = s.DB.Exec("UPDATE site_settings SET settings=?,revision=? WHERE id=1", b, v.Revision)
	}
	if err != nil {
		s.siteMu.Unlock()
		return err
	}
	s.site = v
	s.siteMu.Unlock()
	s.scanControlMu.Lock()
	if s.scanCancel != nil {
		s.scanCancel()
	}
	s.scanControlMu.Unlock()
	s.audit(u.ID, "scan_settings_update", v.VirusScan.Mode, "ok")
	writeJSON(w, 200, s.scanSettingsResponse())
	return nil
}
func (s *Server) scanTaskContext(parent context.Context) (context.Context, func(), bool) {
	s.scanControlMu.Lock()
	defer s.scanControlMu.Unlock()
	allowed, end := s.scanSettings().window(time.Now())
	if !allowed {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	if !end.IsZero() {
		cancel()
		ctx, cancel = context.WithDeadline(parent, end)
	}
	s.scanCancel = cancel
	return ctx, func() { cancel(); s.scanControlMu.Lock(); s.scanCancel = nil; s.scanControlMu.Unlock() }, true
}
