//go:build windows

package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type uninstallService struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Executable string `json:"executable"`
	Directory  string `json:"directory"`
	Status     string `json:"status"`
	Exists     bool   `json:"exe_exists"`
	Eligible   bool   `json:"eligible"`
}
type uninstallRequest struct {
	Services []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"services"`
	Confirmed bool `json:"confirmed"`
}

func identifyUninstallService(name, path string) (uninstallService, error) {
	v := uninstallService{Name: name, Path: path}
	args, err := windows.DecomposeCommandLine(path)
	if err != nil || len(args) == 0 || !filepath.IsAbs(args[0]) {
		return v, fmt.Errorf("服务启动路径无法识别，请人工核对")
	}
	v.Executable = filepath.Clean(args[0])
	v.Directory = filepath.Dir(v.Executable)
	if name == appService {
		v.Eligible = strings.EqualFold(filepath.Base(v.Executable), "gmpan.exe") && len(args) > 1 && args[1] == "serve"
	} else if name == mysqlService {
		// Only accept the dedicated MySQL bundled by this installer. Other MySQL
		// installations and arbitrary same-name services are never removed.
		bin := filepath.Dir(v.Executable)
		mysql := filepath.Dir(bin)
		components := filepath.Dir(mysql)
		v.Eligible = strings.EqualFold(filepath.Base(v.Executable), "mysqld.exe") && strings.EqualFold(filepath.Base(bin), "bin") && strings.HasPrefix(strings.ToLower(filepath.Base(mysql)), "mysql-8.4.") && strings.EqualFold(filepath.Base(components), "components")
		v.Directory = filepath.Dir(components)
	}
	if info, err := os.Stat(v.Executable); err == nil {
		v.Exists = info.Mode().IsRegular()
	} else if !os.IsNotExist(err) {
		v.Eligible = false
	}
	return v, nil
}
func inspectUninstallServices() ([]uninstallService, error) {
	if !elevated() {
		return nil, fmt.Errorf("请以管理员身份运行安装向导")
	}
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("无法访问 Windows 服务管理器")
	}
	defer m.Disconnect()
	items := []uninstallService{}
	for _, name := range []string{appService, mysqlService} {
		s, err := m.OpenService(name)
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			continue
		}
		if err == windows.ERROR_SERVICE_MARKED_FOR_DELETE {
			items = append(items, uninstallService{Name: name, Status: "已标记删除，请关闭服务窗口后重启电脑"})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("无法读取 %s 服务", name)
		}
		cfg, err := s.Config()
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("无法读取 %s 启动路径", name)
		}
		v, _ := identifyUninstallService(name, cfg.BinaryPathName)
		status, err := s.Query()
		s.Close()
		if err != nil {
			return nil, fmt.Errorf("无法读取 %s 运行状态", name)
		}
		v.Status = "已停止"
		if status.State != svc.Stopped {
			v.Status = "正在运行或等待停止"
		}
		items = append(items, v)
	}
	return items, nil
}
func (w *Wizard) uninstall(p uninstallRequest) {
	defer func() {
		w.mu.Lock()
		w.state.Running = false
		if w.log != nil {
			w.log.Close()
			w.log = nil
		}
		w.mu.Unlock()
	}()
	fail := func(message string) {
		w.step(w.percent(), "卸载停止："+message)
		w.mu.Lock()
		w.state.Error = message + "。数据库、文件和配置均未删除；已移除的服务不会自动恢复。"
		w.mu.Unlock()
	}
	if !elevated() {
		fail("卸载需要本机管理员权限")
		return
	}
	current, err := inspectUninstallServices()
	if err != nil {
		fail(err.Error())
		return
	}
	selected := map[string]uninstallService{}
	for _, item := range p.Services {
		found := false
		for _, actual := range current {
			if actual.Name == item.Name && actual.Path == item.Path && actual.Eligible {
				selected[item.Name] = actual
				found = true
				break
			}
		}
		if !found {
			fail("服务信息已变化或启动路径不属于本系统，请重新检测")
			return
		}
	}
	if len(selected) != len(p.Services) {
		fail("服务选择重复")
		return
	}
	if _, ok := selected[mysqlService]; ok {
		for _, item := range current {
			if item.Name == appService {
				if _, ok := selected[appService]; !ok {
					fail("移除数据库服务前，请同时选择依赖它的 GmPan 服务")
					return
				}
			}
		}
	}
	// Store an administrator-only operation log independently from deleted or
	// unavailable deployment paths. Never read credentials or touch data files.
	logDir := filepath.Join(os.Getenv("ProgramData"), "GmPan", "UninstallLogs")
	if !filepath.IsAbs(logDir) {
		fail("无法确定卸载日志位置")
		return
	}
	if err = localPath(logDir); err != nil {
		fail("卸载日志路径不安全，请检查目录联接及权限")
		return
	}
	if err = os.MkdirAll(logDir, 0700); err != nil {
		fail("无法创建卸载日志目录")
		return
	}
	if err = localPath(logDir); err != nil {
		fail("卸载日志路径不安全")
		return
	}
	if err = protect(logDir); err != nil {
		fail(err.Error())
		return
	}
	logPath := filepath.Join(logDir, "uninstall-"+time.Now().Format("20060102-150405.000000000")+".log")
	w.mu.Lock()
	w.log, err = os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	w.state.Log = logPath
	w.mu.Unlock()
	if err != nil {
		fail("无法创建卸载日志")
		return
	}
	w.step(10, "已核对服务名与启动路径；本操作仅移除服务和对应规则，不删除任何部署数据")
	m, err := mgr.Connect()
	if err != nil {
		fail("无法访问服务管理器")
		return
	}
	defer m.Disconnect()
	for _, name := range []string{appService, mysqlService} {
		item, ok := selected[name]
		if !ok {
			continue
		}
		s, err := m.OpenService(name)
		if err != nil {
			fail("无法打开 " + name + "，请重新检测")
			return
		}
		cfg, err := s.Config()
		if err != nil || cfg.BinaryPathName != item.Path {
			s.Close()
			fail("服务启动路径已变化，请重新检测")
			return
		}
		status, err := s.Query()
		if err == nil && status.State != svc.Stopped {
			if status.State != svc.StopPending {
				_, err = s.Control(svc.Stop)
			}
			if err == nil {
				err = waitService(s, svc.Stopped, 45*time.Second)
			}
		}
		if err != nil {
			s.Close()
			fail("无法停止 " + name + "；请核对是否有其他依赖服务或服务窗口占用")
			return
		}
		if err = s.Delete(); err != nil {
			s.Close()
			fail("无法移除 " + name + " 服务记录")
			return
		}
		s.Close()
		percent := 35
		if name == mysqlService {
			percent = 70
		}
		w.step(percent, "已停止并移除 "+name+" 服务记录；保留目录："+item.Directory)
		if name == appService {
			if err = firewall(item.Directory, 0, false); err != nil {
				w.step(40, "未发现或未能清理对应防火墙规则，请核对规则名："+firewallName(item.Directory))
			} else {
				w.step(40, "已清理该安装对应的 GmPan 防火墙规则")
			}
		}
	}
	remaining, err := inspectUninstallServices()
	if err != nil {
		fail(err.Error())
		return
	}
	for _, item := range remaining {
		if _, ok := selected[item.Name]; ok {
			w.step(95, item.Name+" 仍被 Windows 持有，请关闭服务管理器和属性窗口；必要时重启电脑后再安装")
		}
	}
	w.step(100, "卸载操作已完成。数据库、原始资料、上传文件、配置、证书及日志全部保留；恢复旧站点不能直接重新初始化")
	w.mu.Lock()
	w.state.Done = true
	w.mu.Unlock()
}
