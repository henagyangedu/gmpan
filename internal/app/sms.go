package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func SMSSign(key, secret, timestamp, nonce string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(key + "\n" + timestamp + "\n" + nonce + "\n" + digest(string(body))))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) smsEnabled() bool {
	_, enabled := s.smsSnapshot()
	return enabled
}
func (s *Server) smsRequest(path string, payload any) error {
	c, _ := s.smsSnapshot()
	return s.smsRequestWith(c, path, payload)
}
func (s *Server) smsRequestWith(c SMSConfig, path string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := newID()
	req, e := http.NewRequest("POST", strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gm-AppKey", c.AppKey)
	req.Header.Set("X-Gm-Timestamp", ts)
	req.Header.Set("X-Gm-Nonce", nonce)
	req.Header.Set("X-Gm-Sign", SMSSign(c.AppKey, c.AppSecret, ts, nonce, b))
	resp, e := s.smsClient.Do(req)
	if e != nil {
		var networkError net.Error
		return &smsRequestError{public: apiError{502, "短信平台暂时不可用，请使用密码登录；请勿连续重试"}, timeout: errors.As(e, &networkError) && networkError.Timeout()}
	}
	defer resp.Body.Close()
	var reply struct {
		Code *int `json:"code"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply)
	if resp.StatusCode != 200 || e != nil || reply.Code == nil {
		return &smsRequestError{public: apiError{502, "短信平台返回异常，请稍后再试"}, httpStatus: resp.StatusCode, platformCode: reply.Code}
	}
	if *reply.Code != 0 {
		if path == "/verify-code/check" {
			return apiError{401, "验证码不正确、已使用或已过期"}
		}
		return &smsRequestError{public: apiError{502, "短信未能受理，请联系管理员检查平台配置或额度"}, httpStatus: resp.StatusCode, platformCode: reply.Code}
	}
	return nil
}
func (s *Server) sendSMS(w http.ResponseWriter, r *http.Request, _ *User) error {
	config, enabled := s.smsSnapshot()
	if !enabled {
		return apiError{503, "短信登录尚未配置，请使用密码登录"}
	}
	var p struct {
		Mobile string `json:"mobile"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if !mobilePattern.MatchString(p.Mobile) {
		return bad("请输入有效的手机号")
	}
	block, e := s.limited(p.Mobile, ipKey(r), "sms_send", 5, 20)
	if e != nil {
		return e
	}
	if block {
		return apiError{429, "验证码请求过于频繁，请稍后再试"}
	}
	s.attempt(p.Mobile, ipKey(r), "sms_send")
	var id string
	e = s.DB.QueryRow("SELECT id FROM users WHERE mobile=? AND enabled=1", p.Mobile).Scan(&id)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if id != "" {
		if e = s.reserveSMSCooldown(p.Mobile); e != nil {
			return e
		}
		if e = s.smsRequestWith(config, "/verify-code/send", map[string]string{"scene": config.LoginScene, "mobile": p.Mobile}); e != nil {
			s.audit(id, "sms_send", "", "failed")
			return e
		}
		s.audit(id, "sms_send", "", "accepted")
	}
	writeJSON(w, 200, map[string]any{"message": "如手机号已绑定有效员工账号，验证码将发送至该手机", "cooldown_seconds": 60})
	return nil
}

func (s *Server) reserveSMSCooldown(mobile string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT IGNORE INTO sms_cooldowns(mobile,sent_at) VALUES(?,DATE_SUB(NOW(),INTERVAL 1 DAY))", mobile); err != nil {
		return err
	}
	var sent time.Time
	if err = tx.QueryRow("SELECT sent_at FROM sms_cooldowns WHERE mobile=? FOR UPDATE", mobile).Scan(&sent); err != nil {
		return err
	}
	if time.Since(sent) < 60*time.Second {
		return apiError{429, "请间隔 60 秒后再获取验证码"}
	}
	if _, err = tx.Exec("UPDATE sms_cooldowns SET sent_at=NOW() WHERE mobile=?", mobile); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Server) smsLogin(w http.ResponseWriter, r *http.Request, _ *User) error {
	config, enabled := s.smsSnapshot()
	if !enabled {
		return apiError{503, "短信登录尚未配置，请使用密码登录"}
	}
	var p struct {
		Mobile string `json:"mobile"`
		Code   string `json:"code"`
	}
	if e := decode(w, r, &p); e != nil {
		return e
	}
	if !mobilePattern.MatchString(p.Mobile) || len(p.Code) < 4 || len(p.Code) > 12 {
		return bad("手机号或验证码格式不正确")
	}
	for _, c := range p.Code {
		if c < '0' || c > '9' {
			return bad("验证码格式不正确")
		}
	}
	block, e := s.limited(p.Mobile, ipKey(r), "sms_check", 10, 50)
	if e != nil {
		return e
	}
	if block {
		return apiError{429, "验证过于频繁，请稍后再试"}
	}
	s.attempt(p.Mobile, ipKey(r), "sms_check")
	u, e := scanUser(s.DB.QueryRow("SELECT "+userFields+" FROM users u LEFT JOIN departments d ON d.id=u.dept_id WHERE u.mobile=? AND u.enabled=1", p.Mobile))
	if errors.Is(e, sql.ErrNoRows) {
		return apiError{401, "账号或验证码不正确"}
	}
	if e != nil {
		return e
	}
	if e = s.smsRequestWith(config, "/verify-code/check", map[string]string{"scene": config.LoginScene, "mobile": p.Mobile, "code": p.Code}); e != nil {
		return e
	}
	// Recheck local status after the external operation; the account may have been disabled.
	var accountEnabled bool
	if e = s.DB.QueryRow("SELECT enabled FROM users WHERE id=?", u.ID).Scan(&accountEnabled); e != nil {
		return e
	}
	if !accountEnabled {
		return apiError{401, "账号已停用"}
	}
	if !s.smsEnabled() {
		return apiError{503, "短信登录已关闭，请使用密码登录"}
	}
	return s.session(w, u)
}
