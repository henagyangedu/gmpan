//go:build windows

package setup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Manifest struct {
	MySQLRoot    string            `json:"mysql_root"`
	MySQLVersion string            `json:"mysql_version"`
	Files        map[string]string `json:"sha256"`
}

func readManifest(bundle string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(bundle, "payload", "manifest.json"))
	if err != nil {
		return m, fmt.Errorf("缺少完整环境组件，请使用完整图形部署包")
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("部署组件清单格式不正确")
	}
	if !strings.HasPrefix(m.MySQLVersion, "8.4.") || m.MySQLRoot == "" || strings.ContainsAny(m.MySQLRoot, `/\:`) {
		return m, fmt.Errorf("部署包必须提供 MySQL 8.4 LTS 组件")
	}
	for _, file := range []string{"gmpan.exe", "payload/mysql.zip", "payload/clamav.zip", "payload/vc_redist.x64.exe"} {
		if len(m.Files[file]) != 64 {
			return m, fmt.Errorf("部署清单缺少 %s 校验值", file)
		}
		if info, err := os.Stat(filepath.Join(bundle, filepath.FromSlash(file))); err != nil || !info.Mode().IsRegular() {
			return m, fmt.Errorf("部署包缺少 %s", file)
		}
	}
	return m, nil
}

func verifyPayload(bundle string, m Manifest) error {
	for _, name := range []string{"gmpan.exe", "payload/mysql.zip", "payload/clamav.zip", "payload/vc_redist.x64.exe"} {
		f, err := os.Open(filepath.Join(bundle, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("无法读取部署组件 %s", name)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.Files[name]) {
			return fmt.Errorf("部署文件 %s 校验失败，请重新复制完整部署包", name)
		}
	}
	return nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func extract(source, target string) error {
	r, err := zip.OpenReader(source)
	if err != nil {
		return fmt.Errorf("部署组件压缩包无法打开")
	}
	defer r.Close()
	var total uint64
	if len(r.File) > 20000 {
		return fmt.Errorf("部署组件文件数超限")
	}
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) || strings.Contains(clean, ":") || f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("部署组件包含不安全路径")
		}
		total += f.UncompressedSize64
		if total > 4<<30 {
			return fmt.Errorf("部署组件解压大小超限")
		}
		path := filepath.Join(target, clean)
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(in, int64(f.UncompressedSize64)+1))
		in.Close()
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
