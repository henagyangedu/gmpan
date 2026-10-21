package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type SMSConfig struct {
	BaseURL    string `json:"base_url"`
	AppKey     string `json:"app_key"`
	AppSecret  string `json:"app_secret"`
	LoginScene string `json:"login_scene"`
}

type SMSSettings struct {
	SMSConfig
	Enabled  bool  `json:"enabled"`
	Revision int64 `json:"revision"`
}

func loadSMSSettings(db *sql.DB) (SMSSettings, error) {
	v := SMSSettings{SMSConfig: SMSConfig{BaseURL: "https://sms.gmwang.com.cn/api/v1"}, Revision: 1}
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
		return SMSConfig{}, false
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

func (s *Server) testSMSSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := superAdmin(u); err != nil {
		return err
	}
	var p struct {
		Mobile   string `json:"mobile"`
		Revision int64  `json:"revision"`
	}
	if err := decode(w, r, &p); err != nil {
		return err
	}
	if !mobilePattern.MatchString(p.Mobile) {
		return bad("请输入有效的测试手机号")
	}
	s.smsMu.RLock()
	settings := s.smsSettings
	s.smsMu.RUnlock()
	if p.Revision != settings.Revision {
		return conflict("短信配置已更新，请重新加载、核对并保存后再测试")
	}
	if settings.BaseURL == "" || settings.AppKey == "" || settings.AppSecret == "" || settings.LoginScene == "" {
		return bad("请先保存完整的短信接口基址、AppKey、AppSecret 和登录验证码场景")
	}
	blocked, err := s.limited(u.ID, ipKey(r), "sms_test", 5, 20)
	if err != nil {
		return err
	}
	if blocked {
		return apiError{429, "测试发送过于频繁，同一管理员每 10 分钟最多测试 5 次，请稍后再试"}
	}
	if err := s.reserveSMSCooldown(p.Mobile); err != nil {
		return err
	}
	if _, err := s.DB.Exec("INSERT INTO auth_attempts(subject_hash,ip_hash,purpose) VALUES(?,?,'sms_test')", digest(u.ID), ipKey(r)); err != nil {
		return err
	}
	masked := p.Mobile[:3] + "****" + p.Mobile[7:]
	result := map[string]any{"accepted": true, "message": "平台已受理测试验证码，请检查手机是否收到。受理成功不代表已送达。", "mobile_masked": masked, "cooldown_seconds": 60, "config_revision": settings.Revision, "platform_code": 0, "http_status": 200}
	auditResult := "accepted"
	if err := s.smsRequestWith(settings.SMSConfig, "/verify-code/send", map[string]string{"scene": settings.LoginScene, "mobile": p.Mobile}); err != nil {
		result["accepted"] = false
		result["message"] = "测试发送未完成，请核对已保存的短信配置。不要连续重发。"
		delete(result, "platform_code")
		delete(result, "http_status")
		auditResult = "request_failed"
		var platformError *smsRequestError
		if errors.As(err, &platformError) {
			result["message"] = platformError.diagnostic()
			if platformError.httpStatus != 0 {
				result["http_status"] = platformError.httpStatus
			}
			if platformError.platformCode != nil {
				result["platform_code"] = *platformError.platformCode
				auditResult = "code_" + strconv.Itoa(*platformError.platformCode)
			}
		}
	}
	s.audit(u.ID, "sms_test", masked, auditResult)
	writeJSON(w, 200, result)
	return nil
}
