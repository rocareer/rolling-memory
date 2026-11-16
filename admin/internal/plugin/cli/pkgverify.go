package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// 包校验通道（「完整性」/「包格式」）：`aigo plugin verify <包>` 与 `aigo plugin export`
// 的出包自校验共用同一套判定——staging 解包（zip 防护）→ manifest 闭集校验 →
// files.sha256 逐项比对（清单缺失项 / 比对不一致 / 清单之外夹带 ⇒ 一律拒）→ 签名判定。

// PackageReport 一次包校验的读数
type PackageReport struct {
	// Zip 包文件（本机路径）
	Zip string
	// Bytes 整包字节数（stat）
	Bytes int64
	// Checksum 整包 sha256（来源认证由签名承载；未签名时只作事后回溯）
	Checksum string
	// Entries / ExtractedBytes 实际解出的条目数与字节数
	Entries        int
	ExtractedBytes int64
	// Name / Version / Kind manifest 摘要
	Name    string
	Version string
	Kind    string
	// Files 清单列出的文件数
	Files int
	// Signature 签名判定（Verified=false 且 Fingerprint="" 表示无签名或未验证）
	Signature signatureOutcome
	// SigFile 本次判定所用的签名文件名
	SigFile string
	// SigPresent 源内是否存在签名文件
	SigPresent bool
	// RequiresChecked requires 是否查过台账（selfCheckPackage 不碰库 ⇒ false）
	RequiresChecked bool
}

// Render 打印包校验读数（verify 通道）
func (r *PackageReport) Render(env *Env) {
	env.Printf("✓ staging：%s（整包 sha256 %s，%d 字节）→ 解包 %d 个条目 / %d 字节"+
		"（zip 防护逐条施加：路径穿越 / 符号链接 / 设备文件 / 大小写折叠 + NFC 判重 / 四道上限）\n",
		r.Zip, r.Checksum, r.Bytes, r.Entries, r.ExtractedBytes)
	env.Printf("✓ manifest：%s v%s（kind: %s；包根不承载目录名 ⇒ 「目录名 == name」按落位时构造保证）\n",
		r.Name, r.Version, r.Kind)
	env.Printf("✓ %s：逐项比对通过（%d 个文件；清单缺失项 / 不一致 / 清单之外夹带一律拒）\n", PackManifestFile, r.Files)
	switch {
	case r.Signature.Verified:
		env.Printf("✓ 签名 %s：ed25519 分离签名有效（对象 = %s 的字节；公钥指纹 %s）\n",
			r.SigFile, PackManifestFile, r.Signature.Fingerprint)
	case r.SigPresent:
		env.Printf("⚠ 签名 %s：未验证（本机未登记 %s 受信公钥；--allow-unsigned 已显式接受）\n",
			r.SigFile, TrustedKeysConfigKey)
	default:
		env.Printf("· 包未签名（未签名包须显式 --allow-unsigned 才可安装；checksum 只作事后回溯）\n")
	}
	if !r.RequiresChecked {
		env.Printf("（本通道不查 requires 台账：出包自校验不碰库；依赖查证走 `aigo plugin verify <包>` 与安装链）\n")
	}
}

// VerifyPackage 校验一个插件包（`aigo plugin verify <包.zip>`）：
// staging 解包 → manifest → files.sha256 逐项比对与签名判定（checkSourceIntegrity 同一实现）→
// requires 逐项查台账（非空时，fail-closed：取不到台账即报错，不静默放行）。
func VerifyPackage(ctx context.Context, env *Env, zipPath string, allowUnsigned bool) (*PackageReport, error) {
	staged, err := StageZip(zipPath)
	if err != nil {
		return nil, fmt.Errorf("✗ staging: %w", err)
	}
	defer staged.Cleanup()

	m, warnings, err := manifest.Load(staged.Dir, manifest.Options{
		Prefix:      env.Prefix,
		HostVersion: env.HostVersion,
		// 包根不承载插件名：目录名一致属仓库树不变式，落位时按 manifest.name 构造保证
		IgnoreDirName: true,
	})
	if err != nil {
		return nil, fmt.Errorf("✗ manifest: %w", err)
	}
	//公式校验（F3）：包的 menus 声明与实体页/权限节点公式不符 ⇒ 拒（与安装期同一处校验）
	if _, err := BuildInstallMenus(m.Name, m); err != nil {
		return nil, fmt.Errorf("✗ 菜单/节点公式: %w", err)
	}
	for _, warning := range warnings {
		env.Printf("⚠ %s\n", warning)
	}

	plan := &InstallPlan{Dir: staged.Dir, Manifest: m, FromZip: true, ZipChecksum: staged.Report.Checksum}
	integrity, err := checkSourceIntegrity(ctx, env, plan, integrityOptions{
		RequirePackManifest: true, // 包源必备 files.sha256（包格式；缺失即拒）
		EnforceSignPolicy:   true, // 未签名包须 --allow-unsigned
		AllowUnsigned:       allowUnsigned,
	})
	if err != nil {
		return nil, err
	}

	sigFile := DefaultSignatureFile
	if m.Signature != "" {
		sigFile = m.Signature
	}
	report := &PackageReport{
		Zip:             zipPath,
		Bytes:           staged.Report.Bytes,
		Checksum:        staged.Report.Checksum,
		Entries:         staged.Report.Entries,
		ExtractedBytes:  staged.Report.ExtractedBytes,
		Name:            m.Name,
		Version:         m.Version,
		Kind:            m.Kind,
		Files:           integrity.PackFiles,
		SigFile:         sigFile,
		SigPresent:      integrity.SigPresent,
		Signature:       signatureOutcome{Verified: integrity.SignedBy != "", Fingerprint: integrity.SignedBy},
		RequiresChecked: true,
	}
	return report, nil
}

// selfCheckPackage 出包即自校验（export 用；**不碰库**：只做 staging 解包、清单逐项比对与签名判定，
// 不含 requires 的台账查证——包刚由本地源产出，依赖查证属安装 / verify 通道）。
// 签名判定取 allowUnsigned（export 不代签：源里没有签名就照「未签名」如实报告；有签名则照验，
// 无效签名一样阻断——刚出的包就不通过校验，不能交给下游）。
func selfCheckPackage(env *Env, zipPath, sigFile string) (*PackageReport, error) {
	staged, err := StageZip(zipPath)
	if err != nil {
		return nil, err
	}
	defer staged.Cleanup()

	content, err := os.ReadFile(filepath.Join(staged.Dir, PackManifestFile))
	if err != nil {
		return nil, fmt.Errorf("包内缺 %s: %w", PackManifestFile, err)
	}
	listed, err := ParsePackManifest(content)
	if err != nil {
		return nil, err
	}
	if err := VerifyPackFiles(staged.Dir, listed, sigFile); err != nil {
		return nil, err
	}
	keys, err := LoadTrustedKeys()
	if err != nil {
		return nil, err
	}
	outcome, err := checkSignaturePolicy(env, staged.Dir, sigFile, content, keys, integrityOptions{AllowUnsigned: true})
	if err != nil {
		return nil, err
	}
	_, sigErr := os.Stat(filepath.Join(staged.Dir, filepath.FromSlash(sigFile)))

	report := &PackageReport{
		Zip:            zipPath,
		Bytes:          staged.Report.Bytes,
		Checksum:       staged.Report.Checksum,
		Entries:        staged.Report.Entries,
		ExtractedBytes: staged.Report.ExtractedBytes,
		Files:          len(listed),
		SigFile:        sigFile,
		SigPresent:     sigErr == nil,
		Signature:      outcome,
	}
	return report, nil
}
