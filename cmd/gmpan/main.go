package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gmpan/internal/app"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command == "certificate" {
		return certificate(args)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "config.json", "配置文件")
	username := flags.String("username", "admin", "首个管理员用户名")
	storageRoot := flags.String("root", "", "统一文件存储和读取目录（仅 unify-storage 使用）")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if command != "serve" && command != "migrate" && command != "init-admin" && command != "unify-storage" {
		return fmt.Errorf("可用命令: serve、migrate、init-admin、certificate、unify-storage")
	}
	c, e := app.LoadConfig(*configPath)
	if e != nil {
		return e
	}
	db, e := app.OpenDB(c)
	if e != nil {
		return e
	}
	defer db.Close()
	switch command {
	case "unify-storage":
		if e = app.UnifyStorage(db, c, *storageRoot); e != nil {
			return e
		}
		fmt.Println("目录已统一，原文件保留，索引及配置已更新；请启动新版服务")
		return nil
	case "migrate":
		if e = app.Migrate(db); e != nil {
			return e
		}
		fmt.Println("数据库结构已初始化")
		return nil
	case "init-admin":
		fmt.Fprintln(os.Stderr, "从标准输入读取管理员初始密码（不要把密码放在命令参数中）")
		line, e := bufio.NewReader(os.Stdin).ReadString('\n')
		if e != nil && len(line) == 0 {
			return e
		}
		if e = app.CreateAdmin(db, *username, strings.TrimRight(line, "\r\n"), c.PersonalQuotaBytes); e != nil {
			return e
		}
		fmt.Println("首个管理员已创建")
		return nil
	}
	if e = os.MkdirAll(c.LogDir, 0700); e != nil {
		return e
	}
	logfile := &app.DailyLogWriter{Dir: c.LogDir}
	defer logfile.Close()
	logger := slog.New(slog.NewJSONHandler(logfile, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// A dedicated connection holds the advisory lock for the lifetime of the process.
	lock, e := db.Conn(ctx)
	if e != nil {
		return e
	}
	defer lock.Close()
	var acquired int
	if e = lock.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT('gmpan_',MD5(DATABASE())),0)").Scan(&acquired); e != nil {
		return e
	}
	if acquired != 1 {
		return fmt.Errorf("已有 GmPan 服务连接此数据库，请勿重复启动")
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
				var owns bool
				err := lock.QueryRowContext(checkCtx, "SELECT IS_USED_LOCK(CONCAT('gmpan_',MD5(DATABASE())))=CONNECTION_ID()").Scan(&owns)
				checkCancel()
				if err != nil || !owns {
					logger.Error("database service lock lost; stopping server")
					cancel()
					return
				}
			}
		}
	}()
	s, e := app.NewServer(db, c, logger)
	if e != nil {
		return e
	}
	c = s.Config
	server := siteHTTPServer(s)
	return serve(ctx, cancel, server, s, c, logger)
}
func siteHTTPServer(s *app.Server) *http.Server {
	return &http.Server{Addr: s.Config.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
}
func runHTTP(ctx context.Context, server *http.Server, s *app.Server, c app.Config, logger *slog.Logger) error {
	for {
		if err := app.CheckListenAddress(c.Listen); err != nil {
			return fmt.Errorf("网站端口已被占用或无法监听，请检查服务器端口: %w", err)
		}
		restart := make(chan struct{}, 1)
		s.SetRestartHandler(func() {
			select {
			case restart <- struct{}{}:
			default:
			}
		})
		workerCtx, stopWorkers := context.WithCancel(ctx)
		s.StartWorkers(workerCtx)
		done := make(chan error, 1)
		go func(current *http.Server, config app.Config) {
			if config.TLSCert != "" {
				done <- current.ListenAndServeTLS(config.TLSCert, config.TLSKey)
			} else {
				done <- current.ListenAndServe()
			}
		}(server, c)
		scheme := "http"
		if c.TLSCert != "" {
			scheme = "https"
		}
		logger.Info("server_started", "listen", c.Listen)
		fmt.Println("GmPan: " + scheme + "://" + c.Listen)
		reloading := false
		var listenErr error
		select {
		case <-restart:
			reloading = true
		case <-ctx.Done():
		case listenErr = <-done:
		}
		s.SetRestartHandler(nil)
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		shutdownErr := server.Shutdown(shutdown)
		cancel()
		if shutdownErr != nil {
			_ = server.Close()
		}
		stopWorkers()
		s.WaitWorkers()
		if listenErr != nil && listenErr != http.ErrServerClosed {
			return listenErr
		}
		if shutdownErr != nil && !reloading {
			return shutdownErr
		}
		if shutdownErr != nil {
			logger.Warn("site_restart_closed_active_connections")
		}
		if !reloading || ctx.Err() != nil {
			return nil
		}
		logger.Info("site_restarting")
		next, err := app.NewServer(s.DB, c, logger)
		if err != nil {
			return err
		}
		s = next
		c = next.Config
		server = siteHTTPServer(next)
	}
}
func certificate(args []string) error {
	f := flag.NewFlagSet("certificate", flag.ContinueOnError)
	host := f.String("host", "pan.internal", "内网域名或 IP，可逗号分隔")
	out := f.String("out", "certs", "输出目录")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := os.MkdirAll(*out, 0700); e != nil {
		return e
	}
	for _, n := range []string{"server.crt", "server.key"} {
		if _, e := os.Stat(filepath.Join(*out, n)); e == nil {
			return fmt.Errorf("证书或私钥已存在，不覆盖")
		}
	}
	key, e := rsa.GenerateKey(rand.Reader, 3072)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return e
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "GmPan internal"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	for _, v := range strings.Split(*host, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if ip := net.ParseIP(v); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, v)
		}
	}
	if len(template.IPAddresses)+len(template.DNSNames) == 0 {
		return fmt.Errorf("至少指定一个域名或 IP")
	}
	der, e := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*out, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*out, "server.key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); e != nil {
		return e
	}
	fmt.Println("已生成内网证书；部署时请让客户端信任公钥证书，不要分发私钥")
	return nil
}
