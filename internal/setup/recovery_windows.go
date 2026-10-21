//go:build windows

package setup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gmpan/internal/app"

	"github.com/go-sql-driver/mysql"
)

type recoveryTarget struct {
	Directory string
	Digest    [32]byte
	Ticket    string
}

// Read the chosen file directly: maintenance must not inherit GMPAN_DSN or
// require the site's storage disk, certificate or antivirus to be available.
func recoveryConfig(directory string) (*mysql.Config, [32]byte, error) {
	var zero [32]byte
	if err := localPath(directory); err != nil {
		return nil, zero, err
	}
	path := filepath.Join(directory, "config.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, zero, errors.New("所选目录中没有可读取的原站点 config.json，请选择实际部署目录")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, zero, errors.New("无法读取原站点配置，请以 Windows 管理员身份运行")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	var c app.Config
	if err != nil || len(b) > 64<<10 || json.Unmarshal(b, &c) != nil {
		return nil, zero, errors.New("config.json 格式不正确，请先保留原文件并检查配置")
	}
	cfg, err := mysql.ParseDSN(c.DSN)
	if err != nil {
		return nil, zero, errors.New("原配置中的数据库连接格式不正确")
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	p, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || p < 1 || p > 65535 || cfg.Net != "tcp" || (host != "127.0.0.1" && host != "localhost" && host != "::1") || !databasePattern.MatchString(cfg.DBName) || cfg.User == "" || cfg.MultiStatements || cfg.AllowAllFiles {
		return nil, zero, errors.New("此恢复工具仅支持原配置连接本机 MySQL 的独立 GmPan 数据库；不会连接远程数据库或初始化空库")
	}
	if host == "localhost" {
		cfg.Addr = net.JoinHostPort("127.0.0.1", port)
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 5*time.Second, 5*time.Second
	return cfg, sha256.Sum256(b), nil
}

func recoveryDB(ctx context.Context, cfg *mysql.Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err == nil {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		err = db.PingContext(ctx)
	}
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, errors.New("无法连接原数据库。请保持 MySQL 服务运行，核对原 config.json；不要重新初始化数据库")
	}
	return db, nil
}

func (w *Wizard) recoverPassword(out http.ResponseWriter, r *http.Request) {
	if !elevated() {
		reply(out, 403, map[string]string{"message": "重置超管密码必须在部署电脑上以 Windows 管理员身份运行向导"})
		return
	}
	var p struct {
		Directory       string `json:"directory"`
		Ticket          string `json:"ticket"`
		Username        string `json:"username"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
		Confirmed       bool   `json:"confirmed"`
	}
	d := json.NewDecoder(http.MaxBytesReader(out, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		reply(out, 400, map[string]string{"message": "恢复参数格式不正确"})
		return
	}
	w.mu.Lock()
	if w.state.Running || w.state.Done || w.checking {
		w.mu.Unlock()
		reply(out, 409, map[string]string{"message": "其他操作正在进行，请完成后再恢复密码"})
		return
	}
	w.checking = true
	target := w.recovery
	if r.URL.Path == "/api/setup/recovery-info" {
		w.recovery = nil
	}
	w.mu.Unlock()
	defer func() { w.mu.Lock(); w.checking = false; w.mu.Unlock() }()
	reset := r.URL.Path == "/api/setup/recovery-reset"
	if reset {
		if target == nil || p.Ticket == "" || p.Ticket != target.Ticket {
			reply(out, 409, map[string]string{"message": "请先读取原站点并核对超管账号"})
			return
		}
		p.Directory = target.Directory
		if !p.Confirmed || !usernamePattern.MatchString(p.Username) || p.Password != p.ConfirmPassword {
			reply(out, 400, map[string]string{"message": "请核对账号、确认重置，并输入两次相同的新密码"})
			return
		}
		if !app.ValidPassword(p.Password) {
			reply(out, 400, map[string]string{"message": app.ErrRecoveryPassword.Error()})
			return
		}
	}
	cfg, sum, err := recoveryConfig(p.Directory)
	if err != nil {
		reply(out, 400, map[string]string{"message": err.Error()})
		return
	}
	if reset && sum != target.Digest {
		reply(out, 409, map[string]string{"message": "原配置已发生变化，请重新读取站点并核对账号"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	db, err := recoveryDB(ctx, cfg)
	if err != nil {
		reply(out, 400, map[string]string{"message": err.Error()})
		return
	}
	defer db.Close()
	if !reset {
		type account struct {
			Username string `json:"username"`
			Name     string `json:"name"`
		}
		rows, err := db.QueryContext(ctx, "SELECT username,display_name FROM users WHERE role='admin' AND enabled=1 ORDER BY username")
		if err != nil {
			reply(out, 400, map[string]string{"message": "未找到可读取的 GmPan 账号表。请确认选择的是原站点配置，不要执行初始化"})
			return
		}
		defer rows.Close()
		accounts := []account{}
		for rows.Next() {
			var a account
			if err = rows.Scan(&a.Username, &a.Name); err != nil {
				break
			}
			accounts = append(accounts, a)
		}
		if err != nil || rows.Err() != nil || len(accounts) == 0 {
			reply(out, 400, map[string]string{"message": "未找到启用的超管账号，或账号读取失败；请由维护人员核对原数据库"})
			return
		}
		next := &recoveryTarget{Directory: filepath.Clean(p.Directory), Digest: sum, Ticket: randomSecret()}
		w.mu.Lock()
		w.recovery = next
		w.mu.Unlock()
		reply(out, 200, map[string]any{"directory": next.Directory, "database": cfg.DBName, "accounts": accounts, "ticket": next.Ticket})
		return
	}
	err = app.ResetAdminPassword(ctx, db, p.Username, p.Password)
	if err != nil {
		message := "重置未完成或结果尚未确认。请先尝试新密码登录；若仍失败，核对原数据库权限和服务状态后重新读取站点。不要重新初始化"
		if errors.Is(err, app.ErrRecoveryAccount) || errors.Is(err, app.ErrRecoveryPassword) {
			message = err.Error()
		}
		reply(out, 400, map[string]string{"message": message})
		return
	}
	w.mu.Lock()
	w.recovery = nil
	w.mu.Unlock()
	reply(out, 200, map[string]string{"message": "超管密码已重置，该账号的旧登录已失效。请返回原站点，用所选用户名和新密码重新登录"})
}
