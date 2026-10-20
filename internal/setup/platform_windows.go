//go:build windows

package setup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const appService = "GmPan"
const mysqlService = "GmPanMySQL84"

func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

func EnsureElevated() (bool, error) {
	if elevated() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err := windows.ShellExecute(0, verb, file, nil, dir, windows.SW_SHOWNORMAL); err != nil {
		return false, fmt.Errorf("安装需要管理员权限，请右键安装向导选择“以管理员身份运行”：%w", err)
	}
	return true, nil
}

func OpenURL(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func ShowError(message string) {
	title, _ := windows.UTF16PtrFromString("GmPan 安装向导")
	msg, _ := windows.UTF16PtrFromString(message)
	_, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x10)
}

func command(ctx context.Context, path string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, path, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return c
}

func runCommand(timeout time.Duration, path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return command(ctx, path, args...).Run()
}

func serviceAbsent(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("无法访问 Windows 服务管理器，请以管理员身份运行")
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err == nil {
		s.Close()
		return fmt.Errorf("已存在 %s 服务，本向导只用于新安装；请保留原服务和数据", name)
	}
	if err != windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return fmt.Errorf("无法确认 %s 服务状态", name)
	}
	return nil
}

func createService(name, exe string, args, deps []string) (*mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()
	s, err := m.CreateService(name, exe, mgr.Config{DisplayName: name, Description: "GmPan 公司文件资源管理系统", StartType: mgr.StartManual, ErrorControl: mgr.ErrorNormal, ServiceStartName: `NT SERVICE\` + name, Dependencies: deps, SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED}, args...)
	if err != nil {
		return nil, fmt.Errorf("无法注册 %s 服务，请检查管理员权限和已有服务", name)
	}
	if err = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second}, {Type: mgr.ServiceRestart, Delay: 30 * time.Second}, {Type: mgr.ServiceRestart, Delay: 60 * time.Second}}, 86400); err != nil {
		_ = s.Delete()
		s.Close()
		return nil, fmt.Errorf("无法配置 %s 故障重启策略", name)
	}
	if err = s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		_ = s.Delete()
		s.Close()
		return nil, fmt.Errorf("无法配置 %s 异常退出重启策略", name)
	}
	return s, nil
}

func SaveTestURL(path, url string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err = protect(path); err != nil {
		f.Close()
		return err
	}
	_, err = f.WriteString(url)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func waitService(s *mgr.Service, state svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			return err
		}
		if status.State == state {
			return nil
		}
		if state == svc.Running && status.State == svc.Stopped {
			return fmt.Errorf("服务启动后停止，请查看安装日志和应用日志")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("服务状态检查超时")
}

func stopService(s *mgr.Service) {
	status, err := s.Query()
	if err == nil && status.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		_ = waitService(s, svc.Stopped, 40*time.Second)
	}
}

func automatic(s *mgr.Service, delayed bool) error {
	c, err := s.Config()
	if err != nil {
		return err
	}
	c.StartType = mgr.StartAutomatic
	c.DelayedAutoStart = delayed
	if err = s.UpdateConfig(c); err != nil {
		return err
	}
	c, err = s.Config()
	if err != nil || c.StartType != mgr.StartAutomatic || c.DelayedAutoStart != delayed {
		return fmt.Errorf("服务自启动设置未通过回读检查")
	}
	return nil
}

// The installation directory is new and empty, so replacing inherited ACLs cannot
// revoke rights on pre-existing company documents. Storage roots use grant only.
func protect(path string, grants ...string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("无法检查权限目标：%s", path)
	}
	inherit := "(OI)(CI)"
	if !info.IsDir() {
		inherit = ""
		for i := range grants {
			grants[i] = strings.ReplaceAll(grants[i], "(OI)(CI)", "")
		}
	}
	args := []string{path, "/inheritance:r", "/grant:r", "*S-1-5-18:" + inherit + "F", "*S-1-5-32-544:" + inherit + "F"}
	args = append(args, grants...)
	if err := runCommand(30*time.Second, "icacls.exe", args...); err != nil {
		return fmt.Errorf("目录权限配置失败：%s", path)
	}
	return nil
}

func serviceGrant(name, permission string) (string, error) {
	sid, _, _, err := windows.LookupSID("", `NT SERVICE\`+name)
	if err != nil {
		return "", fmt.Errorf("无法识别专用服务账号 %s", name)
	}
	return "*" + sid.String() + ":(OI)(CI)" + permission, nil
}

func grantStorage(path, grant string) error {
	if err := runCommand(30*time.Second, "icacls.exe", path, "/grant", grant); err != nil {
		return fmt.Errorf("无法向 GmPan 服务授予资料目录读取权限，请检查目录安全设置")
	}
	return nil
}

func firewallName(dir string) string {
	return fmt.Sprintf("GmPan-%x-LAN", sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir)))))[:30]
}

func firewall(dir string, port int, add bool) error {
	args := []string{"advfirewall", "firewall", "delete", "rule", "name=" + firewallName(dir)}
	if add {
		args = []string{"advfirewall", "firewall", "add", "rule", "name=" + firewallName(dir), "dir=in", "action=allow", "protocol=TCP", "remoteip=localsubnet", "profile=domain,private", "program=" + filepath.Join(dir, "gmpan.exe"), "enable=yes"}
	}
	if err := runCommand(30*time.Second, "netsh.exe", args...); err != nil {
		return fmt.Errorf("Windows 防火墙配置失败")
	}
	return nil
}
