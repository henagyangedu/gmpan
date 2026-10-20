package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

type SMSSettings struct {
	SMSConfig
	Enabled  bool  `json:"enabled"`
	Revision int64 `json:"revision"`
}

func loadSMSSettings(db *sql.DB, c Config) (SMSSettings, error) {
	v := SMSSettings{SMSConfig: c.SMS, Enabled: c.SMS.AppKey != "" && c.SMS.AppSecret != "" && c.SMS.LoginScene != "", Revision: 1}
	if v.BaseURL == "" {
		v.BaseURL = "https://sms.gmwang.com.cn/api/v1"
	}
	b, _ := json.Marshal(v)
	if _, e := db.Exec("INSERT IGNORE INTO sms_settings(id,settings) VALUES(1,?)", b); e != nil {
		return v, e
	}
	if e := db.QueryRow("SELECT settings,revision FROM sms_settings WHERE id=1").Scan(&b, &v.Revision); e != nil {
		return v, e
	}
	revision := v.Revision
	if e := json.Unmarshal(b, &v); e != nil {
		return v, e
	}
	v.Revision = revision
	return v, nil
}
func (s *Server) smsSnapshot() (SMSConfig, bool) {
	s.smsMu.RLock()
	defer s.smsMu.RUnlock()
	if s.smsSettings.Revision == 0 {
		c := s.Config.SMS
		return c, c.AppKey != "" && c.AppSecret != "" && c.LoginScene != ""
	}
	v := s.smsSettings
	return v.SMSConfig, v.Enabled && v.AppKey != "" && v.AppSecret != "" && v.LoginScene != ""
}
func (s *Server) smsSettingsResponse() map[string]any {
	v := s.smsSettings
	return map[string]any{"enabled": v.Enabled, "base_url": v.BaseURL, "login_scene": v.LoginScene, "has_app_key": v.AppKey != "", "has_app_secret": v.AppSecret != "", "revision": v.Revision}
}
func (s *Server) getSMSSettings(w http.ResponseWriter, _ *http.Request, u *User) error {
	if e := superAdmin(u); e != nil {
		return e
	}
	s.smsMu.RLock()
	defer s.smsMu.RUnlock()
	writeJSON(w, 200, s.smsSettingsResponse())
	return nil
}

func (s *Server) saveSMSSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	if e := superAdmin(u); e != nil {
		return e
	}
	var p struct {
		SMSSettings
		ClearCredentials bool `json:"clear_credentials"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	endpoint, e := url.Parse(p.BaseURL)
	if e != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || len(p.BaseURL) > 500 || strings.ContainsAny(p.BaseURL, "\x00\r\n") {
		return bad("短信平台地址必须是 HTTPS 接口基址，不能包含凭证、查询参数或锚点")
	}
	for _, v := range []string{p.AppKey, p.AppSecret} {
		if len(v) > 256 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
			return bad("短信凭证格式不正确")
		}
	}
	if utf8.RuneCountInString(p.LoginScene) > 64 || strings.TrimSpace(p.LoginScene) != p.LoginScene || strings.ContainsAny(p.LoginScene, "\x00\r\n\t") {
		return bad("登录场景不能超过 64 个字符，也不能包含首尾空白、换行或控制字符")
	}
	s.smsMu.Lock()
	defer s.smsMu.Unlock()
	if p.Revision != s.smsSettings.Revision {
		return conflict("短信配置已更新，请重新加载后保存")
	}
	if !p.ClearCredentials {
		if p.AppKey == "" {
			p.AppKey = s.smsSettings.AppKey
		}
		if p.AppSecret == "" {
			p.AppSecret = s.smsSettings.AppSecret
		}
	} else {
		p.AppKey = ""
		p.AppSecret = ""
	}
	if p.Enabled && (p.AppKey == "" || p.AppSecret == "" || p.LoginScene == "") {
		return bad("启用短信登录前，请填写 AppKey、AppSecret 和已开通的登录场景")
	}
	v := p.SMSSettings
	v.BaseURL = strings.TrimRight(v.BaseURL, "/")
	v.Revision++
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if _, e = s.DB.Exec("UPDATE sms_settings SET settings=?,revision=? WHERE id=1", b, v.Revision); e != nil {
		return e
	}
	s.smsSettings = v
	s.audit(u.ID, "sms_settings_update", "sms", "ok")
	writeJSON(w, 200, s.smsSettingsResponse())
	return nil
}
