package app

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const ChunkSize int64 = 5 * 1024 * 1024

type Config struct {
	ConfigPath         string `json:"-"`
	Listen             string `json:"listen"`
	DSN                string `json:"mysql_dsn"`
	StorageRoot        string `json:"storage_root"`
	DataDir            string `json:"data_dir"`
	LogDir             string `json:"log_dir"`
	LegacyRoot         string `json:"legacy_root"`
	CompanyName        string `json:"company_name"`
	TLSCert            string `json:"tls_cert"`
	TLSKey             string `json:"tls_key"`
	MaxFileBytes       int64  `json:"max_file_bytes"`
	PersonalQuotaBytes int64  `json:"personal_quota_bytes"`
	ClamScanPath       string `json:"clamscan_path"`
	ClamDatabaseDir    string `json:"clam_database_dir"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{Listen: "127.0.0.1:18080", DataDir: "./data", LogDir: "./logs", CompanyName: "公司资源库", MaxFileBytes: 10 << 30, PersonalQuotaBytes: 20 << 30}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if v := os.Getenv("GMPAN_DSN"); v != "" {
		c.DSN = v
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return c, err
	}
	base := filepath.Dir(absolute)
	c.ConfigPath = absolute
	for _, p := range []*string{&c.StorageRoot, &c.DataDir, &c.LogDir, &c.LegacyRoot, &c.TLSCert, &c.TLSKey, &c.ClamDatabaseDir} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	if c.DSN == "" || strings.Contains(c.DSN, "CHANGE_ME") {
		return c, errors.New("请配置专用 MySQL 数据库连接")
	}
	if c.MaxFileBytes <= 0 || c.PersonalQuotaBytes <= 0 {
		return c, errors.New("文件上限和个人配额必须大于零")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return c, errors.New("listen 必须包含地址和端口")
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && (c.TLSCert == "" || c.TLSKey == "") {
		return c, errors.New("内网监听必须配置 HTTPS 证书；本机开发请使用 127.0.0.1")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("HTTPS 证书与私钥必须同时配置")
	}
	if c.StorageRoot == "" && c.LegacyRoot != "" {
		if filepath.Clean(c.LegacyRoot) == filepath.VolumeName(c.LegacyRoot)+string(filepath.Separator) {
			return c, errors.New("历史资料目录不能是磁盘根目录")
		}
		if within(c.LegacyRoot, c.DataDir) || within(c.DataDir, c.LegacyRoot) || within(c.LegacyRoot, c.LogDir) || within(c.LogDir, c.LegacyRoot) {
			return c, errors.New("历史资料目录不能与系统数据或日志目录重叠")
		}
	}
	return c, nil
}

func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func safePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("无效存储路径")
	}
	realRoot, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", e
	}
	p, e := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if e != nil {
		return "", e
	}
	if !within(realRoot, p) {
		return "", errors.New("存储路径超出允许目录")
	}
	return p, nil
}
