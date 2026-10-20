//go:build windows

package setup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"gmpan/internal/app"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{2,31}$`)
var databasePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func (w *Wizard) validate(p Request) error {
	if os.Getenv("GMPAN_DSN") != "" {
		return errors.New("检测到 GMPAN_DSN 环境变量，为避免误连其他数据库，请先由管理员移除此覆盖项后重新打开向导")
	}
	if !elevated() {
		return errors.New("安装需要管理员权限，请右键安装向导选择“以管理员身份运行”")
	}
	if err := localPath(p.Directory); err != nil {
		return err
	}
	if err := localPath(p.Storage); err != nil {
		return err
	}
	if overlaps(p.Directory, p.Storage) || overlaps(p.Directory, w.Bundle) || overlaps(p.Storage, w.Bundle) {
		return errors.New("部署目录、文件目录和解压后的安装包目录必须各自独立，不能互相包含")
	}
	dir, err := os.Open(p.Directory)
	if err == nil {
		entries, readErr := dir.ReadDir(1)
		dir.Close()
		if len(entries) > 0 {
			return errors.New("部署目录不是空目录，请选择新的空文件夹；已有配置和数据库不会覆盖")
		}
		if readErr != nil && readErr != io.EOF {
			return errors.New("部署目录不可读取")
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return errors.New("部署目录不可读取")
	}
	if _, err := os.Lstat(filepath.Join(p.Storage, ".gmpan")); err == nil || !os.IsNotExist(err) {
		return errors.New("资料目录已有 .gmpan 系统数据，本向导不接管或覆盖已有站点，请选择新的资料目录")
	}
	if strings.TrimSpace(p.Company) == "" || utf8.RuneCountInString(p.Company) > 100 || strings.ContainsAny(p.Company, "\x00\r\n") {
		return errors.New("请填写 1～100 字的公司名称")
	}
	if !usernamePattern.MatchString(p.Admin) || len(p.Password) < 10 || len(p.Password) > 72 || strings.ContainsAny(p.Password, "\x00\r\n") {
		return errors.New("超管用户名需以字母开头，共 3～32 位；密码需为 10～72 字节")
	}
	ip := net.ParseIP(p.Host)
	if ip == nil || ip.To4() == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errors.New("请选择本机 IPv4 内网地址或 127.0.0.1")
	}
	if p.Port < 1024 || p.Port > 65535 || p.DBPort < 1024 || p.DBPort > 65535 {
		return errors.New("端口需为 1024～65535")
	}
	if p.DBMode != "new" && p.DBMode != "existing" {
		return errors.New("请选择数据库安装方式")
	}
	if p.DBMode == "new" && p.Port == p.DBPort {
		return errors.New("网站和数据库端口不能相同")
	}
	if p.DBMode == "existing" && (p.DBHost != "127.0.0.1" || !databasePattern.MatchString(p.DBName) || p.DBUser == "" || p.DBPassword == "") {
		return errors.New("连接已有数据库时，请提供本机独立空库、专用账号和密码；本版不连接远程数据库")
	}
	if err := serviceAbsent(appService); err != nil {
		return err
	}
	if p.DBMode == "new" {
		if err := serviceAbsent(mysqlService); err != nil {
			return err
		}
	}
	l, err := net.Listen("tcp4", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	if err != nil {
		return errors.New("网站端口已占用或所选 IP 不属于本机")
	}
	l.Close()
	if p.DBMode == "new" {
		l, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.DBPort)))
		if err != nil {
			return errors.New("数据库端口已占用，请换一个端口")
		}
		l.Close()
	}
	if _, err := readManifest(w.Bundle); err != nil {
		return err
	}
	if p.DBMode == "existing" {
		db, err := connectDB(dbConfig(p))
		if err != nil {
			return errors.New("数据库连接失败，请核对服务、端口、空库、专用账号及密码")
		}
		defer db.Close()
		if err := requireEmptyDB(db); err != nil {
			return err
		}
	}
	return nil
}

func dbConfig(p Request) *mysql.Config {
	c := mysql.NewConfig()
	c.User = p.DBUser
	c.Passwd = p.DBPassword
	c.Net = "tcp"
	c.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.DBPort))
	c.DBName = p.DBName
	c.ParseTime = true
	c.Loc = time.Local
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 30 * time.Second
	c.WriteTimeout = 30 * time.Second
	c.Params = map[string]string{"charset": "utf8mb4"}
	return c
}

func connectDB(c *mysql.Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func requireEmptyDB(db *sql.DB) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&n); err != nil {
		return errors.New("无法核对数据库是否为空")
	}
	if n != 0 {
		return errors.New("目标数据库已有数据，安装已停止；本向导只初始化专用空库，不会覆盖或升级已有数据库")
	}
	return nil
}

func (w *Wizard) install(p Request) {
	var installed []*mgr.Service
	firewallAdded := false
	success := false
	defer func() {
		for i := len(installed) - 1; i >= 0; i-- {
			s := installed[i]
			if !success {
				stopService(s)
				_ = s.Delete()
			}
			s.Close()
		}
		if !success && firewallAdded {
			_ = firewall(p.Directory, p.Port, false)
		}
		w.mu.Lock()
		w.state.Running = false
		if w.log != nil {
			w.log.Close()
			w.log = nil
		}
		w.mu.Unlock()
	}()
	fail := func(message string) {
		w.step(w.percent(), "安装停止："+message)
		w.mu.Lock()
		w.state.Error = message + "。已创建的服务会停止并撤销，文件和数据库保留，避免丢失资料。请查看日志；重试请使用新的空部署目录。"
		w.mu.Unlock()
	}
	if err := w.validate(p); err != nil {
		fail(err.Error())
		return
	}
	w.step(5, "目录、端口、数据库及服务冲突检查通过")
	m, err := readManifest(w.Bundle)
	if err != nil {
		fail(err.Error())
		return
	}
	if err = verifyPayload(w.Bundle, m); err != nil {
		fail(err.Error())
		return
	}
	w.step(10, "完整部署组件 SHA256 校验通过")
	if err = os.MkdirAll(p.Directory, 0700); err != nil {
		fail("无法创建部署目录")
		return
	}
	if err = protect(p.Directory); err != nil {
		fail(err.Error())
		return
	}
	if err = os.MkdirAll(filepath.Join(p.Directory, "logs"), 0700); err != nil {
		fail("无法创建日志目录")
		return
	}
	logPath := filepath.Join(p.Directory, "logs", "setup.log")
	w.mu.Lock()
	w.log, err = os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	w.state.Log = logPath
	w.mu.Unlock()
	if err != nil {
		fail("无法创建安装日志")
		return
	}
	if err = copyFile(filepath.Join(w.Bundle, "gmpan.exe"), filepath.Join(p.Directory, "gmpan.exe")); err != nil {
		fail("复制服务程序失败")
		return
	}
	for _, name := range []string{"LICENSE", "SPONSORSHIP.md", "系统部署操作手册.md", "用户操作手册.md", "公司文件资源管理系统需求.md", "图形部署测试说明.md", "THIRD-PARTY-NOTICES.md"} {
		if _, err := os.Stat(filepath.Join(w.Bundle, name)); err == nil {
			if err = copyFile(filepath.Join(w.Bundle, name), filepath.Join(p.Directory, name)); err != nil {
				fail("复制部署说明失败")
				return
			}
		}
	}
	if images, readErr := os.ReadDir(filepath.Join(w.Bundle, "images")); readErr == nil {
		if err = os.Mkdir(filepath.Join(p.Directory, "images"), 0700); err != nil {
			fail("创建手册配图目录失败")
			return
		}
		for _, image := range images {
			if image.IsDir() || (strings.ToLower(filepath.Ext(image.Name())) != ".jpg" && strings.ToLower(filepath.Ext(image.Name())) != ".png") {
				continue
			}
			if err = copyFile(filepath.Join(w.Bundle, "images", image.Name()), filepath.Join(p.Directory, "images", image.Name())); err != nil {
				fail("复制手册配图失败")
				return
			}
		}
	}
	components := filepath.Join(p.Directory, "components")
	if err = extract(filepath.Join(w.Bundle, "payload", "clamav.zip"), components); err != nil {
		fail("ClamAV 组件解压失败：" + err.Error())
		return
	}
	w.step(20, "文件安全检查程序和离线病毒库已准备")
	var mysqlPath, rootPassword string
	if p.DBMode == "new" {
		if err = extract(filepath.Join(w.Bundle, "payload", "mysql.zip"), components); err != nil {
			fail("MySQL 组件解压失败：" + err.Error())
			return
		}
		mysqlPath = filepath.Join(components, m.MySQLRoot)
		if err = runCommand(15*time.Second, filepath.Join(mysqlPath, "bin", "mysqld.exe"), "--no-defaults", "--version"); err != nil {
			w.step(25, "正在安装 Microsoft Visual C++ x64 运行库")
			err = runCommand(5*time.Minute, filepath.Join(w.Bundle, "payload", "vc_redist.x64.exe"), "/install", "/quiet", "/norestart")
			// A restart-required exit is acceptable only if the actual MySQL executable now works.
			if check := runCommand(15*time.Second, filepath.Join(mysqlPath, "bin", "mysqld.exe"), "--no-defaults", "--version"); check != nil {
				fail("MySQL 运行库未就绪，请重启电脑后使用新空目录重试")
				return
			}
		}
		p.DBName, p.DBUser, p.DBPassword = "gmpan", "gmpan", randomSecret()
		rootPassword = randomSecret()
		w.step(30, "MySQL 8.4 LTS 已解压，正在创建独立数据库")
		if err = w.bootstrapMySQL(p, mysqlPath, rootPassword); err != nil {
			fail(err.Error())
			return
		}
	}
	config := app.Config{Listen: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), DSN: dbConfig(p).FormatDSN(), StorageRoot: p.Storage, LogDir: filepath.Join(p.Directory, "logs"), CompanyName: p.Company, MaxFileBytes: 10 << 30, PersonalQuotaBytes: 20 << 30, ClamScanPath: filepath.Join(components, "clamav", "clamscan.exe"), ClamDatabaseDir: filepath.Join(components, "clamav", "database")}
	if !net.ParseIP(p.Host).IsLoopback() {
		certDir := filepath.Join(p.Directory, "certs")
		if err = runCommand(30*time.Second, filepath.Join(p.Directory, "gmpan.exe"), "certificate", "-host", p.Host+",127.0.0.1,localhost", "-out", certDir); err != nil {
			fail("内网 HTTPS 证书生成失败")
			return
		}
		config.TLSCert, config.TLSKey = filepath.Join(certDir, "server.crt"), filepath.Join(certDir, "server.key")
	}
	if err = os.MkdirAll(p.Storage, 0700); err != nil {
		fail("无法创建资料目录")
		return
	}
	var mysqlSvc *mgr.Service
	if p.DBMode == "new" {
		mysqlSvc, err = createService(mysqlService, filepath.Join(mysqlPath, "bin", "mysqld.exe"), []string{"--defaults-file=" + filepath.Join(p.Directory, "mysql.ini"), mysqlService}, nil)
		if err != nil {
			fail(err.Error())
			return
		}
		installed = append(installed, mysqlSvc)
		mysqlRead, err := serviceGrant(mysqlService, "RX")
		if err != nil {
			fail(err.Error())
			return
		}
		if err = protect(p.Directory, mysqlRead); err != nil {
			fail(err.Error())
			return
		}
		grant, err := serviceGrant(mysqlService, "M")
		if err != nil {
			fail(err.Error())
			return
		}
		if err = protect(filepath.Join(p.Directory, "mysql-data"), grant); err != nil {
			fail(err.Error())
			return
		}
		if err = protect(filepath.Join(p.Directory, "mysql-tmp"), grant); err != nil {
			fail(err.Error())
			return
		}
		if err = mysqlSvc.Start(); err != nil {
			fail("MySQL 服务启动失败")
			return
		}
		if err = waitService(mysqlSvc, svc.Running, 60*time.Second); err != nil {
			fail("MySQL 服务未进入运行状态，请查看 mysql-data 中的错误日志")
			return
		}
	}
	db, err := waitDB(dbConfig(p), 60*time.Second)
	if err != nil {
		fail("数据库未就绪，请检查独立数据库服务和连接权限")
		return
	}
	defer db.Close()
	if err = requireEmptyDB(db); err != nil {
		fail(err.Error())
		return
	}
	if err = app.Migrate(db); err != nil {
		fail("初始化数据表失败，请检查空库账号的建表权限")
		return
	}
	if err = app.CreateAdmin(db, p.Admin, p.Password, config.PersonalQuotaBytes); err != nil {
		fail("超管创建失败，请检查用户名和密码要求")
		return
	}
	w.step(60, "数据表和超管已初始化，短信登录默认关闭")
	configPath := filepath.Join(p.Directory, "config.json")
	b, _ := json.MarshalIndent(config, "", "  ")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		fail("无法保存部署配置")
		return
	}
	config, err = app.LoadConfig(configPath)
	if err != nil {
		fail("生成的配置未通过校验")
		return
	}
	if _, err = app.NewServer(db, config, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		fail("站点和文件目录初始化失败，请检查目录是否独立且可写")
		return
	}
	// Check real scanner and virus database; do not bypass the application's content gate.
	probe := filepath.Join(p.Directory, "logs", "scan-probe.txt")
	if err = os.WriteFile(probe, []byte("GmPan setup scanner check\n"), 0600); err != nil {
		fail("安全检查测试文件创建失败")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	status, _ := (app.CommandScanner{Path: config.ClamScanPath, DatabaseDir: config.ClamDatabaseDir}).Check(ctx, probe)
	cancel()
	_ = os.Remove(probe)
	if status != "clean" {
		fail("安全检查未通过，请检查 ClamAV 程序和病毒库；未关闭或绕过安全检查")
		return
	}
	w.step(70, "内网证书、文件目录和 ClamAV 实际扫描检查通过")
	if p.DBMode == "new" {
		if err = limitRuntimeDB(p, rootPassword); err != nil {
			fail("数据库运行账号权限收紧失败")
			return
		}
	}
	deps := []string{}
	if p.DBMode == "new" {
		deps = append(deps, mysqlService)
	}
	appSvc, err := createService(appService, filepath.Join(p.Directory, "gmpan.exe"), []string{"serve", "-config", configPath}, deps)
	if err != nil {
		fail(err.Error())
		return
	}
	installed = append(installed, appSvc)
	appRead, err := serviceGrant(appService, "RX")
	if err != nil {
		fail(err.Error())
		return
	}
	appWrite, err := serviceGrant(appService, "M")
	if err != nil {
		fail(err.Error())
		return
	}
	rootGrants := []string{appRead}
	if p.DBMode == "new" {
		mysqlRead, err := serviceGrant(mysqlService, "RX")
		if err != nil {
			fail(err.Error())
			return
		}
		rootGrants = append(rootGrants, mysqlRead)
	}
	if err = protect(p.Directory, rootGrants...); err != nil {
		fail(err.Error())
		return
	}
	if err = protect(configPath, appRead); err != nil {
		fail(err.Error())
		return
	}
	if config.TLSCert != "" {
		if err = protect(filepath.Dir(config.TLSCert), appRead); err != nil {
			fail(err.Error())
			return
		}
	}
	if err = protect(config.LogDir, appWrite); err != nil {
		fail(err.Error())
		return
	}
	if err = grantStorage(p.Storage, appRead); err != nil {
		fail(err.Error())
		return
	}
	if err = protect(filepath.Join(p.Storage, ".gmpan"), appWrite); err != nil {
		fail(err.Error())
		return
	}
	if err = appSvc.Start(); err != nil {
		fail("GmPan 服务启动失败，请检查专用服务账号的目录权限")
		return
	}
	if err = waitService(appSvc, svc.Running, 60*time.Second); err != nil {
		fail(err.Error())
		return
	}
	scheme := "http"
	if config.TLSCert != "" {
		scheme = "https"
	}
	url := scheme + "://" + config.Listen
	if err = health(url, config.TLSCert); err != nil {
		fail("站点健康检查失败，请查看应用日志")
		return
	}
	w.step(85, "GmPan 已以专用低权限服务账号启动，站点健康检查通过")
	if p.Firewall && !net.ParseIP(p.Host).IsLoopback() {
		if err = firewall(p.Directory, p.Port, true); err != nil {
			fail(err.Error())
			return
		}
		firewallAdded = true
	}
	if mysqlSvc != nil {
		if err = automatic(mysqlSvc, false); err != nil {
			fail("MySQL 开机自启动配置失败")
			return
		}
	}
	if err = automatic(appSvc, true); err != nil {
		fail("GmPan 开机自启动配置失败")
		return
	}
	guide := fmt.Sprintf("GmPan 安装结果\r\n访问地址：%s\r\n超管账号：%s\r\n超管密码：使用安装时自行设置的密码，不在此记录\r\n资料目录：%s\r\n安装日志：%s\r\n服务：GmPan（自动，延迟启动）\r\n短信：登录超管后台 → 站点配置 → 短信配置\r\n", url, p.Admin, p.Storage, logPath)
	if p.DBMode == "new" {
		guide += "数据库服务：GmPanMySQL84（自动）\r\n数据库维护凭据：database-recovery.txt（仅系统管理员可读，妥善备份）\r\n"
	}
	if config.TLSCert != "" {
		guide += "内网证书：certs/server.crt，客户端需按公司流程信任；绝不能分发 server.key\r\n"
	}
	guide += "首次登录后由管理员检查目录，再扫描公司已有资料；安装不会自动公开扫描。\r\n请在目标电脑完成一次重启测试，确认两个服务自动恢复。\r\n"
	if err = os.WriteFile(filepath.Join(p.Directory, "安装结果.txt"), []byte(guide), 0600); err != nil {
		fail("无法保存安装结果")
		return
	}
	w.step(100, "安装完成，开机自启动设置已回读确认；请打开站点登录并测试文件操作")
	w.mu.Lock()
	w.state.Done = true
	w.state.URL = url
	w.mu.Unlock()
	success = true
}

func (w *Wizard) percent() int { w.mu.Lock(); defer w.mu.Unlock(); return w.state.Percent }

func waitDB(c *mysql.Config, timeout time.Duration) (*sql.DB, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		db, err := connectDB(c)
		if err == nil {
			return db, nil
		}
		var serverError *mysql.MySQLError
		if errors.As(err, &serverError) && serverError.Number == 1045 {
			return nil, errors.New("数据库本机账号认证失败，请检查专用账号和主机权限")
		}
		time.Sleep(time.Second)
	}
	return nil, errors.New("数据库就绪检查超时")
}

func (w *Wizard) bootstrapMySQL(p Request, mysqlPath, rootPassword string) error {
	data := filepath.Join(p.Directory, "mysql-data")
	tmp := filepath.Join(p.Directory, "mysql-tmp")
	for _, path := range []string{data, tmp} {
		if err := os.Mkdir(path, 0700); err != nil {
			return errors.New("无法创建独立 MySQL 数据和临时目录")
		}
	}
	ini := fmt.Sprintf("[mysqld]\nbasedir=\"%s\"\ndatadir=\"%s\"\ntmpdir=\"%s\"\nbind-address=127.0.0.1\nport=%d\nmysqlx=OFF\nskip-name-resolve\ncharacter-set-server=utf8mb4\ncollation-server=utf8mb4_unicode_ci\nlog-error=\"%s\"\ninnodb-buffer-pool-size=128M\nmax-connections=50\n", filepath.ToSlash(mysqlPath), filepath.ToSlash(data), filepath.ToSlash(tmp), p.DBPort, filepath.ToSlash(filepath.Join(data, "mysql-error.log")))
	iniPath := filepath.Join(p.Directory, "mysql.ini")
	if err := os.WriteFile(iniPath, []byte(ini), 0600); err != nil {
		return errors.New("MySQL 配置保存失败")
	}
	exe := filepath.Join(mysqlPath, "bin", "mysqld.exe")
	if err := runCommand(5*time.Minute, exe, "--defaults-file="+iniPath, "--initialize-insecure"); err != nil {
		return errors.New("MySQL 数据目录初始化失败，请查看 mysql-data/mysql-error.log")
	}
	// Generated hex secrets cannot inject SQL. The init-file runs before accepting
	// TCP clients, so no listening server is ever exposed with an empty root password.
	initPath := filepath.Join(p.Directory, "mysql-bootstrap.sql")
	statements := fmt.Sprintf("ALTER USER 'root'@'localhost' IDENTIFIED BY '%s';\nCREATE USER 'root'@'127.0.0.1' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;\nCREATE DATABASE gmpan CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\nCREATE USER 'gmpan'@'127.0.0.1' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON gmpan.* TO 'gmpan'@'127.0.0.1';\n", rootPassword, rootPassword, p.DBPassword)
	if err := os.WriteFile(initPath, []byte(statements), 0600); err != nil {
		return errors.New("数据库安全初始化准备失败")
	}
	defer os.Remove(initPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	process := command(ctx, exe, "--defaults-file="+iniPath, "--init-file="+initPath, "--no-monitor")
	if err := process.Start(); err != nil {
		return errors.New("MySQL 安全初始化进程无法启动")
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	defer func() {
		select {
		case <-done:
		default:
			_ = process.Process.Kill()
			<-done
		}
	}()
	root := dbConfig(p)
	root.User = "root"
	root.Passwd = rootPassword
	root.DBName = ""
	db, err := waitDB(root, 60*time.Second)
	if err != nil {
		return errors.New("MySQL 安全初始化未就绪，请查看 mysql-data/mysql-error.log")
	}
	defer db.Close()
	if _, err = db.Exec("SHUTDOWN"); err != nil {
		return errors.New("MySQL 初始化进程未能安全停止")
	}
	select {
	case err := <-done:
		done <- err
		if err != nil {
			return errors.New("MySQL 初始化进程异常结束")
		}
	case <-time.After(30 * time.Second):
		return errors.New("MySQL 初始化进程退出超时")
	}
	recovery := fmt.Sprintf("GmPan 独立数据库维护凭据（机密，仅系统管理员）\r\n主机：127.0.0.1:%d\r\n管理员：root\r\n密码：%s\r\n数据库：gmpan\r\n组件目录：%s\r\n请妥善备份；不要发到聊天、仓库或截图。\r\n", p.DBPort, rootPassword, mysqlPath)
	if err = os.WriteFile(filepath.Join(p.Directory, "database-recovery.txt"), []byte(recovery), 0600); err != nil {
		return errors.New("数据库维护凭据保存失败")
	}
	// Keep this file administrator-only even after service read access is granted.
	if err = protect(filepath.Join(p.Directory, "database-recovery.txt")); err != nil {
		return err
	}
	return nil
}

func limitRuntimeDB(p Request, rootPassword string) error {
	c := dbConfig(p)
	c.User = "root"
	c.Passwd = rootPassword
	c.DBName = ""
	db, err := connectDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.Exec("REVOKE ALL PRIVILEGES ON gmpan.* FROM 'gmpan'@'127.0.0.1'"); err != nil {
		return err
	}
	_, err = db.Exec("GRANT SELECT, INSERT, UPDATE, DELETE ON gmpan.* TO 'gmpan'@'127.0.0.1'")
	return err
}

func health(url, certPath string) error {
	transport := &http.Transport{}
	if certPath != "" {
		b, err := os.ReadFile(certPath)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return errors.New("证书无效")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get(url + "/api/health")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			var v map[string]any
			if res.StatusCode == 200 && json.Unmarshal(body, &v) == nil && v["status"] == "ok" {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("站点健康检查超时")
}
