package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const storageMarker = "GmPan managed storage v1\n"

func deploymentDirectory() string {
	executable, e := os.Executable()
	if e != nil {
		return ""
	}
	if real, e := filepath.EvalSymlinks(executable); e == nil {
		executable = real
	}
	return filepath.Dir(executable)
}
func unifiedConfig(c Config, root string) Config {
	c.StorageRoot = filepath.Clean(root)
	c.DataDir = filepath.Join(c.StorageRoot, ".gmpan")
	c.LegacyRoot = c.StorageRoot
	return c
}
func managedObjectKey(scope, owner, dept, key string) string {
	switch scope {
	case "company":
		return "company/" + key
	case "department":
		return filepath.ToSlash(filepath.Join("department", dept, key))
	case "personal":
		return filepath.ToSlash(filepath.Join("personal", owner, key))
	}
	return ""
}
func realIfExisting(p string) string {
	if real, e := filepath.EvalSymlinks(p); e == nil {
		return real
	}
	return filepath.Clean(p)
}
func validateStorageRoot(root string, c Config) error {
	if !filepath.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") {
		return bad("文件存储和读取目录必须是绝对路径")
	}
	real, e := canonicalRoot(root)
	if e != nil {
		return bad("文件目录不存在或无法读取，请先在服务器创建目录")
	}
	if filepath.Dir(real) == real {
		return bad("文件目录不能是磁盘根目录")
	}
	for _, protected := range []string{deploymentDirectory(), c.LogDir} {
		if protected != "" {
			p := realIfExisting(protected)
			if within(real, p) || within(p, real) {
				return bad("文件目录必须与站点部署目录、日志目录分开")
			}
		}
	}
	for _, protected := range []string{c.ConfigPath, c.TLSCert, c.TLSKey} {
		if protected != "" && within(real, realIfExisting(protected)) {
			return bad("文件目录不能包含系统配置或证书文件")
		}
	}
	managed := filepath.Join(real, ".gmpan")
	if info, e := os.Lstat(managed); e == nil {
		actual, e := filepath.EvalSymlinks(managed)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !sameDirectory(actual, managed) {
			return bad("系统内部目录 .gmpan 不能是文件、链接或目录联接")
		}
		marker, e := os.ReadFile(filepath.Join(managed, "storage.marker"))
		if e != nil {
			entries, readErr := os.ReadDir(managed)
			if readErr != nil || len(entries) > 0 {
				return bad("指定目录已存在非本系统管理的 .gmpan 文件夹，请先核对")
			}
		} else if string(marker) != storageMarker {
			return bad("系统存储目录标记不正确")
		}
	} else if !os.IsNotExist(e) {
		return bad("无法访问系统内部目录")
	}
	return nil
}
func prepareStorageRoot(root string, c Config) error {
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	if e := validateStorageRoot(root, c); e != nil {
		return e
	}
	base := filepath.Join(root, ".gmpan")
	if e := os.MkdirAll(base, 0700); e != nil {
		return e
	}
	marker := filepath.Join(base, "storage.marker")
	if _, e := os.Stat(marker); os.IsNotExist(e) {
		f, e := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString(storageMarker)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, name := range []string{"objects", "chunks", "migration"} {
		p := filepath.Join(base, name)
		if e := os.MkdirAll(p, 0700); e != nil {
			return e
		}
		if !sameDirectory(realIfExisting(p), p) {
			return bad("系统内部存储子目录不能是链接或目录联接")
		}
	}
	probe, e := os.CreateTemp(base, ".write-check-*")
	if e != nil {
		return bad("服务账号没有文件存储目录写入权限")
	}
	name := probe.Name()
	if e = probe.Close(); e != nil {
		return e
	}
	if e = os.Remove(name); e != nil {
		return bad("服务账号没有文件存储目录临时文件删除权限")
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Copies are verified and never replace a different target file. Original files remain intact.
func copyStorageFile(source, target, temporary string) (fs.FileInfo, error) {
	// Refuse links in any existing target ancestor before creating directories or files.
	for p := target; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil && !sameDirectory(realIfExisting(p), p) {
			return nil, fmt.Errorf("目标路径含链接或目录联接: %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	before, e := os.Stat(source)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("只能迁移普通文件")
	}
	sourceHash, e := hashFile(source)
	if e != nil {
		return nil, e
	}
	if existing, e := os.Stat(target); e == nil {
		targetHash, e := hashFile(target)
		if e != nil || sourceHash != targetHash {
			return nil, fmt.Errorf("目标已存在不同内容的文件，迁移已停止: %s", target)
		}
		return existing, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return nil, e
	}
	input, e := os.Open(source)
	if e != nil {
		return nil, e
	}
	defer input.Close()
	output, e := os.CreateTemp(temporary, "copy-*")
	if e != nil {
		return nil, e
	}
	name := output.Name()
	defer os.Remove(name)
	defer output.Close()
	if _, e = io.Copy(output, input); e != nil {
		return nil, e
	}
	if e = output.Sync(); e != nil {
		return nil, e
	}
	if e = output.Close(); e != nil {
		return nil, e
	}
	after, e := os.Stat(source)
	if e != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fmt.Errorf("迁移期间源文件发生变化")
	}
	actual, e := hashFile(name)
	if e != nil || actual != sourceHash {
		return nil, fmt.Errorf("迁移文件校验失败")
	}
	if e = os.Chtimes(name, before.ModTime(), before.ModTime()); e != nil {
		return nil, e
	}
	// Exclusive creation also works on volumes without hard-link support.
	verified, e := os.Open(name)
	if e != nil {
		return nil, e
	}
	defer verified.Close()
	final, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	complete := false
	defer func() {
		final.Close()
		if !complete {
			os.Remove(target)
		}
	}()
	if _, e = io.Copy(final, verified); e != nil {
		return nil, e
	}
	if e = final.Sync(); e != nil {
		return nil, e
	}
	if e = final.Close(); e != nil {
		return nil, e
	}
	actual, e = hashFile(target)
	if e != nil || actual != sourceHash {
		return nil, fmt.Errorf("目标文件校验失败")
	}
	if e = os.Chtimes(target, before.ModTime(), before.ModTime()); e != nil {
		return nil, e
	}
	complete = true
	return os.Stat(target)
}
func copyStorageTree(source, target, temporary string, public bool) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		if relative == "." {
			return nil
		}
		if public && filepath.Dir(relative) == "." && strings.EqualFold(d.Name(), ".gmpan") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if public && (d.Name() == ".git" || d.Name() == ".svn" || d.Name() == "$RECYCLE.BIN" || d.Name() == "System Volume Information") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil || d.Type()&os.ModeSymlink != 0 || !sameDirectory(real, path) || !within(source, real) {
			return fmt.Errorf("迁移源目录含链接或不可定位的文件，请先核对: %s", path)
		}
		dest := filepath.Join(target, relative)
		if !within(target, dest) {
			return fmt.Errorf("无效目标路径")
		}
		if d.IsDir() {
			if e = os.MkdirAll(dest, 0700); e != nil {
				return e
			}
			if !sameDirectory(realIfExisting(dest), dest) {
				return fmt.Errorf("目标目录含链接")
			}
			return nil
		}
		_, e = copyStorageFile(path, dest, temporary)
		return e
	})
}

// UnifyStorage requires a stopped server, copies data, then atomically changes the index/config.
// A failed copy leaves old settings authoritative; retrying reuses only byte-identical copies.
func UnifyStorage(db *sql.DB, c Config, target string) error {
	if !filepath.IsAbs(target) {
		return bad("请用 -root 指定绝对路径")
	}
	lock, e := db.Conn(context.Background())
	if e != nil {
		return e
	}
	defer lock.Close()
	var acquired int
	if e = lock.QueryRowContext(context.Background(), "SELECT GET_LOCK(CONCAT('gmpan_',MD5(DATABASE())),0)").Scan(&acquired); e != nil {
		return e
	}
	if acquired != 1 {
		return conflict("请先停止 GmPan 服务，再迁移文件目录")
	}
	defer lock.ExecContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT('gmpan_',MD5(DATABASE())))")
	settings, _, e := loadSiteSettings(db, c)
	if e != nil {
		return e
	}
	old := c
	if settings.StorageRoot != "" {
		old = unifiedConfig(old, settings.StorageRoot)
	} else {
		old.DataDir, old.LegacyRoot = settings.DataDir, settings.LegacyRoot
		old.StorageRoot = ""
	}
	target, e = canonicalRoot(target)
	if e != nil {
		return bad("目标目录不存在，请先创建")
	}
	if settings.StorageRoot != "" && sameDirectory(settings.StorageRoot, target) {
		return nil
	}
	var foreign int
	if e = db.QueryRow("SELECT COUNT(*) FROM entries WHERE storage_kind='legacy' AND source_root_hash<>?", sourceHash(old.LegacyRoot, "")).Scan(&foreign); e != nil {
		return e
	}
	if foreign != 0 {
		return conflict("存在其它来源目录的索引，请先核对原读取目录再迁移")
	}
	for _, previous := range []string{old.DataDir, old.LegacyRoot} {
		if previous == "" {
			continue
		}
		previous = realIfExisting(previous)
		if sameDirectory(previous, target) && sameDirectory(previous, old.LegacyRoot) {
			continue
		}
		if within(previous, target) || within(target, previous) {
			return bad("迁移目标不能与旧目录相互包含；可使用原公司资料目录，或独立的新目录")
		}
	}
	if e = prepareStorageRoot(target, c); e != nil {
		return e
	}
	temporary := filepath.Join(target, ".gmpan", "migration")
	if old.LegacyRoot != "" && !sameDirectory(realIfExisting(old.LegacyRoot), target) {
		if e = copyStorageTree(realIfExisting(old.LegacyRoot), target, temporary, true); e != nil {
			return e
		}
	}
	chunks := filepath.Join(old.DataDir, "chunks")
	if _, e = os.Stat(chunks); e == nil {
		if e = copyStorageTree(realIfExisting(chunks), filepath.Join(target, ".gmpan", "chunks"), temporary, false); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	rows, e := db.Query("SELECT " + entryFields + " FROM entries e LEFT JOIN users u ON u.id=e.owner_id WHERE storage_kind IN ('local','legacy')")
	if e != nil {
		return e
	}
	entries := []*Entry{}
	for rows.Next() {
		v, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	type update struct {
		entry *Entry
		key   string
		mtime int64
	}
	updates := []update{}
	for _, v := range entries {
		if v.StorageKind == "legacy" {
			if v.Kind == "file" && !v.SourceMissing {
				source, err := safePath(old.LegacyRoot, v.StoragePath)
				if err != nil {
					return fmt.Errorf("原资料不可读取，请先重新扫描（%s）", v.ID)
				}
				info, err := os.Stat(source)
				if err != nil || info.Size() != v.Size || info.ModTime().UnixNano() != v.SourceMtime {
					return fmt.Errorf("原资料已变化，请先重新扫描（%s）", v.ID)
				}
				dest, err := safePath(target, v.StoragePath)
				if err != nil {
					return err
				}
				info, err = os.Stat(dest)
				if err != nil {
					return err
				}
				updates = append(updates, update{v, v.StoragePath, info.ModTime().UnixNano()})
			}
			continue
		}
		key := v.StoragePath
		if old.StorageRoot == "" {
			key = managedObjectKey(v.Scope, v.OwnerID, v.DeptID, key)
		}
		if key == "" {
			return bad("旧文件空间类型无效")
		}
		source, e := safePath(filepath.Join(old.DataDir, "objects"), v.StoragePath)
		if e != nil {
			return fmt.Errorf("上传文件不可读取，迁移已停止（%s）", v.ID)
		}
		original, e := os.Stat(source)
		if e != nil || original.Size() != v.Size || original.ModTime().UnixNano() != v.SourceMtime {
			return fmt.Errorf("上传文件与原索引不一致，请先检修（%s）", v.ID)
		}
		if v.ScanStatus == "clean" && v.SHA256 != "" {
			hash, err := hashFile(source)
			if err != nil || hash != v.SHA256 {
				return fmt.Errorf("上传文件与安全检查记录不一致（%s）", v.ID)
			}
		}
		if filepath.IsAbs(key) || !within(filepath.Join(target, ".gmpan", "objects"), filepath.Join(target, ".gmpan", "objects", filepath.FromSlash(key))) {
			return bad("旧文件存储路径无效")
		}
		info, e := copyStorageFile(source, filepath.Join(target, ".gmpan", "objects", filepath.FromSlash(key)), temporary)
		if e != nil {
			return e
		}
		if info.Size() != v.Size {
			return fmt.Errorf("上传文件大小与索引不一致（%s）", v.ID)
		}
		updates = append(updates, update{v, key, info.ModTime().UnixNano()})
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, v := range entries {
		if v.StorageKind != "legacy" {
			continue
		}
		if _, e = tx.Exec("UPDATE entries SET source_key=?,source_root_hash=? WHERE id=?", sourceHash(target, v.StoragePath), sourceHash(target, ""), v.ID); e != nil {
			return e
		}
	}
	for _, v := range updates {
		if _, e = tx.Exec("UPDATE entries SET storage_path=?,source_mtime=? WHERE id=?", v.key, v.mtime, v.entry.ID); e != nil {
			return e
		}
	}
	settings.StorageRoot = target
	settings.DataDir = ""
	settings.LegacyRoot = ""
	settings.Revision++
	encoded, e := json.Marshal(settings)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE site_settings SET settings=?,revision=? WHERE id=1", encoded, settings.Revision); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO audit_logs(action,target_id,result) VALUES('storage_unify','site','ok')"); e != nil {
		return e
	}
	return tx.Commit()
}
