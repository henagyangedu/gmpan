package app

import (
	"crypto/hmac"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

const smsCodeLifetime = 5 * time.Minute

type smsLoginCode struct {
	id, userID, salt, hash string
	expires                time.Time
	failures               int
	ready                  bool
	configRevision         int64
}

func newSMSCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (s *Server) sendTemplateCode(config SMSConfig, mobile, code, requestID string) error {
	return s.smsRequestWith(config, "/sms/send", struct {
		TemplateKey string            `json:"templateKey"`
		Mobile      string            `json:"mobile"`
		Variables   map[string]string `json:"variables"`
		RequestID   string            `json:"requestId"`
	}{config.TemplateKey, mobile, map[string]string{"code": code}, requestID})
}

func (s *Server) sendLoginCode(config SMSConfig, mobile, userID string) error {
	code, err := newSMSCode()
	if err != nil {
		return err
	}
	v := smsLoginCode{id: newID(), userID: userID, salt: newID(), expires: time.Now().Add(smsCodeLifetime), configRevision: config.revision}
	v.hash = digest(v.salt + code)
	s.smsCodeMu.Lock()
	if s.smsCodes == nil {
		s.smsCodes = make(map[string]smsLoginCode)
	}
	for phone, previous := range s.smsCodes {
		if !time.Now().Before(previous.expires) {
			delete(s.smsCodes, phone)
		}
	}
	s.smsCodes[mobile] = v
	s.smsCodeMu.Unlock()
	err = s.sendTemplateCode(config, mobile, code, v.id)
	s.smsCodeMu.Lock()
	defer s.smsCodeMu.Unlock()
	current, exists := s.smsCodes[mobile]
	if !exists || current.id != v.id {
		if err != nil {
			return err
		}
		return conflict("短信配置已更新，请稍后重新获取验证码")
	}
	if err != nil {
		delete(s.smsCodes, mobile)
		return err
	}
	v.ready = true
	s.smsCodes[mobile] = v
	return nil
}

func (s *Server) consumeSMSCode(mobile, userID, code string) bool {
	config, enabled := s.smsSnapshot()
	if !enabled {
		return false
	}
	s.smsCodeMu.Lock()
	defer s.smsCodeMu.Unlock()
	v, exists := s.smsCodes[mobile]
	if !exists {
		return false
	}
	if !time.Now().Before(v.expires) || v.configRevision != config.revision {
		delete(s.smsCodes, mobile)
		return false
	}
	if !v.ready || v.userID != userID {
		return false
	}
	if !hmac.Equal([]byte(v.hash), []byte(digest(v.salt+code))) {
		v.failures++
		if v.failures >= 5 {
			delete(s.smsCodes, mobile)
		} else {
			s.smsCodes[mobile] = v
		}
		return false
	}
	delete(s.smsCodes, mobile)
	return true
}
