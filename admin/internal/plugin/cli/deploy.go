package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Deployer 一次安装的落位执行器（mkdir -p + 原子 rename 落盘；落位与删除期间 airx.Pause）。
//
// 逐文件落位，并**记录**每个目标在落位前的状态（新建 / 覆盖 + 备份）：补偿回滚据此把文件面
// 还原到落位前的样子（新建的删除、覆盖的从备份还原），而不是只做「删已落位文件」——
// 后者会把被 --force 覆盖的既有内容一并抹掉。
type Deployer struct {
	env       *Env
	name      string
	version   string
	backupDir string
	files     []placedFile
	dirs      []string
}

// placedFile 单个落位目标的前后状态
type placedFile struct {
	// Target 仓库相对路径
	Target string
	// Created 目标原先不存在（回滚 = 删除）
	Created bool
	// Backup 覆盖前的内容备份（回滚 = 还原）；空 = 原先不存在
	Backup string
}

// NewDeployer 构造落位执行器（备份目录为一次性临时目录）
func NewDeployer(env *Env, plan *InstallPlan) (*Deployer, error) {
	dir, err := os.MkdirTemp("", "aigo-plugin-backup-")
	if err != nil {
		return nil, fmt.Errorf("创建落位备份目录失败: %w", err)
	}
	return &Deployer{env: env, name: plan.Name, version: plan.Manifest.Version, backupDir: dir}, nil
}

// Place 落位全部文件（plan.Files 的逐个目标；同一目标的多个来源按计划顺序覆盖，最后写入者为准）
func (d *Deployer) Place(plan *InstallPlan) error {
	for i, file := range plan.Files {
		if err := d.placeOne(plan, file, i); err != nil {
			return err
		}
	}
	return nil
}

// placeOne 落位单个文件：逐级软链检查 → 备份既有 → 写归属标记 → 同目录临时文件 + 原子 rename
func (d *Deployer) placeOne(plan *InstallPlan, file PlanFile, index int) error {
	if file.Rel == "" {
		return fmt.Errorf("落位计划缺源路径（%s）：计划不完整", file.Target)
	}
	if err := checkRelPathSafe(file.Target); err != nil {
		return err
	}
	if err := ensureNoSymlink(d.env.Root, file.Target); err != nil {
		return err
	}

	source := filepath.Join(plan.Dir, filepath.FromSlash(file.Rel))
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("读取源文件 %s 失败: %w", file.Rel, err)
	}
	perm := fs.FileMode(0o644)
	if info, err := os.Stat(source); err == nil && info.Mode().Perm() != 0 {
		perm = info.Mode().Perm()
	}
	content = ApplyOwnershipMarker(file.Rel, content, d.name, d.version)

	target := d.env.Abs(file.Target)
	placed := placedFile{Target: file.Target}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("落位目标 %s 是符号链接：拒装（落位侧逐级拒软链）", file.Target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("落位目标 %s 不是普通文件（%s）：拒装", file.Target, info.Mode().String())
		}
		backup, err := d.backup(file, index)
		if err != nil {
			return err
		}
		placed.Backup = backup
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("检查落位目标 %s 失败: %w", file.Target, err)
	} else {
		placed.Created = true
	}

	if err := d.mkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	if err := atomicWriteFile(target, content, perm); err != nil {
		return fmt.Errorf("落位 %s 失败: %w", file.Target, err)
	}
	d.files = append(d.files, placed)
	return nil
}

// backup 备份将被覆盖的既有文件（同一目标多次出现时只备份一次，保留最早的内容）
func (d *Deployer) backup(file PlanFile, index int) (string, error) {
	for i := range d.files {
		if d.files[i].Target == file.Target && d.files[i].Backup != "" {
			return d.files[i].Backup, nil
		}
	}
	backup := filepath.Join(d.backupDir, fmt.Sprintf("%04d-%s", index, filepath.Base(file.Target)))
	if err := copyFile(d.env.Abs(file.Target), backup); err != nil {
		return "", fmt.Errorf("备份既有文件 %s 失败: %w", file.Target, err)
	}
	return backup, nil
}

// mkdirAll 逐级创建目录（记录本次新建的层级，供回滚清理）
func (d *Deployer) mkdirAll(dir string) error {
	rel, err := filepath.Rel(d.env.Root, dir)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	segments := strings.Split(path.Clean(rel), "/")
	cur := d.env.Root
	for _, segment := range segments {
		cur = filepath.Join(cur, segment)
		if info, err := os.Lstat(cur); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("落位路径的上级 %s 是符号链接：拒装", cur)
			}
			if !info.IsDir() {
				return fmt.Errorf("落位路径的上级 %s 不是目录", cur)
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("检查目录 %s 失败: %w", cur, err)
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", cur, err)
		}
		d.dirs = append(d.dirs, filepath.ToSlash(relPathOf(d.env.Root, cur)))
	}
	return nil
}

// Rollback 补偿回滚文件面（逆序）：新建的删除、覆盖的从备份还原、本次新建的空目录清除。
// 逐项尽力而为并汇总失败：**任一失败即视为补偿失败**（调用方须保留现场、状态留 failed）。
func (d *Deployer) Rollback() error {
	var failures []string
	for i := len(d.files) - 1; i >= 0; i-- {
		file := d.files[i]
		target := d.env.Abs(file.Target)
		if file.Created {
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				failures = append(failures, fmt.Sprintf("删除 %s: %v", file.Target, err))
			}
			continue
		}
		content, err := os.ReadFile(file.Backup)
		if err != nil {
			failures = append(failures, fmt.Sprintf("读取备份（%s）: %v", file.Target, err))
			continue
		}
		if err := atomicWriteFile(target, content, 0o644); err != nil {
			failures = append(failures, fmt.Sprintf("还原 %s: %v", file.Target, err))
		}
	}
	for i := len(d.dirs) - 1; i >= 0; i-- {
		if err := os.Remove(d.env.Abs(d.dirs[i])); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// 目录非空属正常（其中有其它插件/手工文件），不计失败
			if !isDirNotEmpty(err) {
				failures = append(failures, fmt.Sprintf("删除空目录 %s: %v", d.dirs[i], err))
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("文件面回滚未完成：\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}

// Discard 清理备份目录（落位成功、无需回滚时调用）
func (d *Deployer) Discard() {
	if d.backupDir != "" {
		_ = os.RemoveAll(d.backupDir)
		d.backupDir = ""
	}
}

// checkRelPathSafe 落位目标必须是仓库内的规范化相对路径（`.`,`..` 段一律拒）
func checkRelPathSafe(rel string) error {
	clean := path.Clean(filepath.ToSlash(rel))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || clean != filepath.ToSlash(rel) {
		return fmt.Errorf("落位目标 %q 不是仓库内规范化相对路径：拒装", rel)
	}
	return nil
}

// ensureNoSymlink 逐级检查落位目标的**每一级既有目录**不得是符号链接（落位侧）
func ensureNoSymlink(root, rel string) error {
	if err := checkRelPathSafe(rel); err != nil {
		return err
	}
	segments := strings.Split(path.Clean(filepath.ToSlash(rel)), "/")
	cur := root
	for i := 0; i < len(segments)-1; i++ { // 最后一段是目标文件本身，单独判定
		cur = filepath.Join(cur, segments[i])
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			continue // 尚未创建：后续 mkdirAll 自建，不存在穿越面
		}
		if err != nil {
			return fmt.Errorf("检查落位路径 %s 的上级 %s 失败: %w", rel, cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("落位路径 %s 的上级 %s 是符号链接：拒装（「落位目标的每一级目录不得是既有符号链接」）", rel, cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("落位路径 %s 的上级 %s 不是目录", rel, cur)
		}
	}
	return nil
}

// atomicWriteFile 同目录临时文件 + rename 落盘（原子；不跨文件系统）
func atomicWriteFile(target string, content []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// copyFile 复制普通文件（备份用）
func copyFile(source, target string) error {
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o600)
}

// relPathOf 绝对路径 → 仓库相对路径（/ 分隔）
func relPathOf(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return full
	}
	return filepath.ToSlash(rel)
}

// isDirNotEmpty 判定「目录非空」类错误（空目录清理时的正常分支）
func isDirNotEmpty(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		// syscall.ENOTEMPTY / EEXIST 在不同平台下字符串不同，按系统错误判定
		return errors.Is(err, fs.ErrExist) || strings.Contains(strings.ToLower(err.Error()), "not empty") ||
			strings.Contains(strings.ToLower(err.Error()), "directory not empty")
	}
	return false
}

// RemoveTree 删除插件归属的目录/文件（卸载 b 用）：拒绝符号链接与越出仓库的路径；
// 返回是否删除（不存在 ⇒ false，不算失败）。
func (env *Env) RemoveTree(rel string) (bool, error) {
	if err := checkRelPathSafe(rel); err != nil {
		return false, err
	}
	full := env.Abs(rel)
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查 %s 失败: %w", rel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s 是符号链接：拒删（卸载按目录归属清理，不跟随软链）", rel)
	}
	if err := os.RemoveAll(full); err != nil {
		return false, fmt.Errorf("删除 %s 失败: %w", rel, err)
	}
	return true, nil
}

// RemoveFile 删除单个文件（卸载 b 的语言包用；不存在 ⇒ false）
func (env *Env) RemoveFile(rel string) (bool, error) {
	if err := checkRelPathSafe(rel); err != nil {
		return false, err
	}
	full := env.Abs(rel)
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查 %s 失败: %w", rel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s 是符号链接：拒删（不跟随软链）", rel)
	}
	if err := os.Remove(full); err != nil {
		return false, fmt.Errorf("删除 %s 失败: %w", rel, err)
	}
	return true, nil
}

// PruneEmptyDir 逐级清空空目录（停在 stopAt 中的任一级；不存在或非空即停，不报错）
func (env *Env) PruneEmptyDir(rel string, stopAt ...string) {
	blocked := make(map[string]bool, len(stopAt))
	for _, item := range stopAt {
		blocked[path.Clean(filepath.ToSlash(item))] = true
	}
	clean := path.Clean(filepath.ToSlash(rel))
	for clean != "." && clean != "/" && !blocked[clean] {
		full := env.Abs(clean)
		if info, err := os.Lstat(full); err != nil || !info.IsDir() {
			return
		}
		entries, err := os.ReadDir(full)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(full); err != nil {
			return
		}
		clean = path.Dir(clean)
	}
}
