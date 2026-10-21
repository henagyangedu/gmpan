//go:build windows

// Package setup implements the local Windows installation wizard.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gmpan/internal/app"
	"gmpan/internal/webui"
)

type Request struct {
	Directory  string `json:"directory"`
	Storage    string `json:"storage"`
	Company    string `json:"company"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	PlainHTTP  bool   `json:"plain_http"`
	DBMode     string `json:"db_mode"`
	DBHost     string `json:"db_host"`
	DBPort     int    `json:"db_port"`
	DBName     string `json:"db_name"`
	DBUser     string `json:"db_user"`
	DBPassword string `json:"db_password"`
	Admin      string `json:"admin"`
	Password   string `json:"password"`
	Firewall   bool   `json:"firewall"`
}

type Progress struct {
	Operation string   `json:"operation"`
	Running   bool     `json:"running"`
	Done      bool     `json:"done"`
	Percent   int      `json:"percent"`
	Steps     []string `json:"steps"`
	Error     string   `json:"error"`
	URL       string   `json:"url"`
	Log       string   `json:"log"`
	Directory string   `json:"directory"`
}

type Wizard struct {
	Bundle   string
	Token    string
	Host     string
	mu       sync.Mutex
	state    Progress
	checking bool
	recovery *recoveryTarget
	log      *os.File
	Quit     func()
}

func New(bundle string) *Wizard {
	return &Wizard{Bundle: bundle, Token: randomSecret(), state: Progress{Steps: []string{}}}
}

func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (w *Wizard) Handler() http.Handler {
	files, _ := fs.Sub(webui.Files, "dist")
	assets := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Cache-Control", "no-store")
		out.Header().Set("X-Content-Type-Options", "nosniff")
		out.Header().Set("Referrer-Policy", "no-referrer")
		out.Header().Set("X-Frame-Options", "DENY")
		out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(remote) == nil || !net.ParseIP(remote).IsLoopback() || r.Host != w.Host {
			http.Error(out, "仅允许本机访问", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Setup-Token")), []byte(w.Token)) != 1 || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+w.Host) {
				http.Error(out, "请从安装向导重新打开页面", http.StatusForbidden)
				return
			}
			w.api(out, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			out.WriteHeader(405)
			return
		}
		if r.URL.Path == "/" {
			r.URL.Path = "/setup.html"
		}
		if r.URL.Path != "/setup.html" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(out, r)
			return
		}
		assets.ServeHTTP(out, r)
	})
}

func reply(out http.ResponseWriter, status int, value any) {
	out.Header().Set("Content-Type", "application/json; charset=utf-8")
	out.WriteHeader(status)
	_ = json.NewEncoder(out).Encode(value)
}

func (w *Wizard) api(out http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "POST /api/setup/recovery-info", "POST /api/setup/recovery-reset":
		w.recoverPassword(out, r)
	case "GET /api/setup/check-port":
		host := r.URL.Query().Get("host")
		ip := net.ParseIP(host)
		port, err := strconv.Atoi(r.URL.Query().Get("port"))
		if err != nil || port < 1 || port > 65535 || ip == nil || ip.To4() == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			reply(out, 400, map[string]string{"message": "请选择本机内网 IPv4 地址和有效网站端口"})
			return
		}
		w.mu.Lock()
		busy := w.state.Running || w.state.Done || w.checking
		w.mu.Unlock()
		if busy {
			reply(out, 409, map[string]string{"message": "当前操作正在进行，请完成后再检查端口"})
			return
		}
		err = app.CheckListenAddress(net.JoinHostPort(host, strconv.Itoa(port)))
		message := "端口可用，安装时还会再次检查"
		if err != nil {
			message = "该端口已被占用或无法监听，请选择备用端口，或由管理员检查占用程序"
		}
		reply(out, 200, map[string]any{"available": err == nil, "message": message})
	case "GET /api/setup/uninstall-info":
		services, err := inspectUninstallServices()
		if err != nil {
			reply(out, 400, map[string]string{"message": err.Error()})
			return
		}
		reply(out, 200, map[string]any{"services": services})
	case "POST /api/setup/uninstall":
		var p uninstallRequest
		decoder := json.NewDecoder(http.MaxBytesReader(out, r.Body, 32<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF || !p.Confirmed || len(p.Services) < 1 || len(p.Services) > 2 {
			reply(out, 400, map[string]string{"message": "请确认卸载范围，并选择需要移除的服务"})
			return
		}
		w.mu.Lock()
		if w.state.Running || w.state.Done || w.checking {
			w.mu.Unlock()
			reply(out, 409, map[string]string{"message": "其他检查或安装正在进行，请先完成当前操作"})
			return
		}
		w.state = Progress{Running: true, Operation: "uninstall", Steps: []string{}}
		w.mu.Unlock()
		go w.uninstall(p)
		reply(out, 202, map[string]string{"message": "正在卸载服务，保留全部数据文件"})
	case "GET /api/setup/info":
		root := `C:\`
		if _, err := os.Stat(`D:\`); err == nil {
			root = `D:\`
		}
		hosts := []string{"127.0.0.1"}
		interfaces, _ := net.InterfaceAddrs()
		for _, addr := range interfaces {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() {
				hosts = append(hosts, ip.String())
			}
		}
		_, payloadErr := readManifest(w.Bundle)
		reply(out, 200, map[string]any{"directory": filepath.Join(root, "GmPan"), "storage": filepath.Join(root, "GmPanFiles"), "hosts": hosts, "app_port": freePort(18080), "db_port": freePort(13306), "payload_ready": payloadErr == nil, "elevated": elevated(), "version": "Windows 图形部署测试版 · MySQL 8.4 LTS"})
	case "GET /api/setup/progress":
		w.mu.Lock()
		defer w.mu.Unlock()
		reply(out, 200, w.state)
	case "GET /api/setup/directories":
		w.mu.Lock()
		busy := w.state.Running || w.state.Done
		w.mu.Unlock()
		if busy {
			reply(out, 409, map[string]string{"message": "安装已启动，无法修改目录"})
			return
		}
		w.browseDirectories(out, r)
	case "POST /api/setup/exit":
		w.mu.Lock()
		busy := w.state.Running || w.checking
		w.mu.Unlock()
		if busy {
			reply(out, 409, map[string]string{"message": "当前操作尚在运行，请等待完成或失败处理结束"})
			return
		}
		reply(out, 200, map[string]string{"message": "安装向导已关闭"})
		if w.Quit != nil {
			go func() { time.Sleep(300 * time.Millisecond); w.Quit() }()
		}
	case "POST /api/setup/check", "POST /api/setup/install":
		var p Request
		decoder := json.NewDecoder(http.MaxBytesReader(out, r.Body, 32<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			reply(out, 400, map[string]string{"message": "安装参数格式不正确"})
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			reply(out, 400, map[string]string{"message": "安装参数格式不正确"})
			return
		}
		w.mu.Lock()
		if w.state.Running || w.state.Done || w.checking {
			w.mu.Unlock()
			reply(out, 409, map[string]string{"message": "环境检查或安装正在运行，请等待当前操作完成"})
			return
		}
		if r.URL.Path == "/api/setup/install" {
			w.state = Progress{Running: true, Operation: "install", Steps: []string{}, Directory: p.Directory}
			w.mu.Unlock()
			go w.install(p)
			reply(out, 202, map[string]string{"message": "安装已开始"})
			return
		}
		w.checking = true
		w.mu.Unlock()
		// Filesystem and SCM calls may stall on an unhealthy target computer.
		// Keep only one outstanding check and always return control to the page.
		result := make(chan error, 1)
		go func() {
			defer func() { w.mu.Lock(); w.checking = false; w.mu.Unlock() }()
			result <- w.validate(p)
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		select {
		case err := <-result:
			if err != nil {
				reply(out, 400, map[string]string{"message": err.Error()})
				return
			}
			reply(out, 200, map[string]string{"message": "检查通过，可开始安装。数据库和超管密码不会写入安装日志。"})
		case <-ctx.Done():
			reply(out, 504, map[string]string{"message": "环境检查超时，尚未开始安装。请检查目录所在磁盘、Windows 服务状态；连接已有数据库时也请核对数据库服务。若重试仍提示检查正在运行，请关闭并重新打开向导。"})
		}
	default:
		http.NotFound(out, r)
	}
}

func freePort(start int) int {
	for port := start; port < start+100; port++ {
		if app.CheckListenAddress(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) == nil {
			return port
		}
	}
	return start
}

func (w *Wizard) step(percent int, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Percent = percent
	w.state.Steps = append(w.state.Steps, message)
	if w.log != nil {
		_, _ = fmt.Fprintf(w.log, "%s %s\n", time.Now().Format(time.RFC3339), message)
	}
}

func localPath(path string) error {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || len(filepath.VolumeName(path)) != 2 || strings.ContainsAny(path, "\x00\r\n\"%") || filepath.Clean(path) == filepath.VolumeName(path)+`\` {
		return errors.New("请选择本机磁盘中的独立文件夹，不能是磁盘根目录或网络路径")
	}
	// Reject junctions/symlinks even in ancestors; an elevated installer must not follow them.
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil {
			actual, e := filepath.EvalSymlinks(p)
			if e != nil || info.Mode()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Clean(actual), p) {
				return errors.New("部署和资料路径不能经过符号链接或目录联接")
			}
			if !info.IsDir() {
				return errors.New("所选路径被同名文件占用")
			}
		} else if !os.IsNotExist(err) {
			return errors.New("无法检查所选目录权限")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

func overlaps(a, b string) bool {
	a, b = strings.ToLower(filepath.Clean(a)), strings.ToLower(filepath.Clean(b))
	return a == b || strings.HasPrefix(a, b+`\`) || strings.HasPrefix(b, a+`\`)
}
