package cli

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
)

// 签名面（「完整性」行）：分离签名（默认 plugin.yaml.sig）的**签名对象 = 包内清单
// files.sha256 的字节**——只签 plugin.yaml 会让 migrations 与源码逃出覆盖面。
//
// 判定口径（fail-closed，不静默降级）：
//   - 无签名：包源（EnforceSignPolicy）须显式 `--allow-unsigned`；目录源默认放行（开发通道）；
//   - 有签名 + 本机登记了受信公钥：逐钥验证——通过则记指纹（落 `admin_plugins.signed_by`），
//     不通过 ⇒ **拒装**（签名无效是篡改证据：`--allow-unsigned` 只覆盖「没有签名 / 无法验证」，
//     不豁免「签名无效」）；
//   - 有签名 + 本机未登记公钥：无法验证 ⇒ 须显式 `--allow-unsigned`（此时 signed_by 留空——
//     不校验就不声称指纹）。
//
// 受信公钥清单来自配置键 `plugin.trusted_keys`（config/*.yaml / .env.yaml / 环境变量
// PLUGIN_TRUSTED_KEYS 皆可，见 internal/infra/config 的三层合并）：每项为 ed25519 公钥，
// hex（64 字符）或 base64（32 字节），可带 `ed25519:` 前缀。**公钥指纹**（signed_by 落库值）
// = `sha256:` + SHA256(公钥 32 字节) 的 hex。
//
// 边界（如实）：签名只保证「装的就是签的那个包」——不覆盖安装后的文件篡改
//（L1 代码最终进入编译产物，安装后完整性无意义）。

// TrustedKeysConfigKey 受信公钥清单的配置键（`plugin.trusted_keys`）
const TrustedKeysConfigKey = "plugin.trusted_keys"

// signatureOutcome 一次签名判定（signed_by 落库值的来源）
type signatureOutcome struct {
	// Verified 签名对 files.sha256 校验通过（本机登记了受信公钥且逐钥验证命中）
	Verified bool
	// Fingerprint 通过校验的公钥指纹（`sha256:<hex>`）；未通过校验时为空——不声称指纹
	Fingerprint string
}

// Fingerprint 公钥指纹（signed_by 落库值；长度 71 字符，落 varchar(128)）
func Fingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LoadTrustedKeys 读取本机受信公钥清单（配置键 plugin.trusted_keys）。
// 未配置 ⇒ 返回空清单（语义是「本机无法验证任何签名」，调用方据此走 --allow-unsigned 分支）。
func LoadTrustedKeys() ([]ed25519.PublicKey, error) {
	entries := trustedKeyEntries()
	keys := make([]ed25519.PublicKey, 0, len(entries))
	for _, entry := range entries {
		key, err := ParsePublicKey(entry)
		if err != nil {
			return nil, fmt.Errorf("%s 的项 %q 不是可用的 ed25519 公钥（%w）；"+
				"注意：指纹（sha256:…）无法验签，请填公钥本身", TrustedKeysConfigKey, entry, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// trustedKeyEntries 取配置里的公钥项原文（配置系统未初始化 ⇒ 空清单；换行/逗号/空白分隔都拆开，
// 这样 yaml 列表、单行字符串与环境变量三种写了都能用）
func trustedKeyEntries() []string {
	if !config.Initialized() {
		return nil
	}
	vip := config.Viper()
	if vip == nil {
		return nil
	}
	var out []string
	for _, item := range vip.GetStringSlice(TrustedKeysConfigKey) {
		for _, part := range strings.FieldsFunc(item, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
		}) {
			part = strings.TrimSpace(part)
			if part == "" || strings.HasPrefix(part, "#") {
				continue
			}
			out = append(out, part)
		}
	}
	return out
}

// ParsePublicKey 解析一个 ed25519 公钥（hex 64 字符 / base64 32 字节，可带 `ed25519:` 前缀）
func ParsePublicKey(raw string) (ed25519.PublicKey, error) {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "ed25519:"))
	if text == "" {
		return nil, errors.New("值为空")
	}
	if len(text) == 2*ed25519.PublicKeySize && isHex(text) {
		data, err := hex.DecodeString(text)
		if err == nil && len(data) == ed25519.PublicKeySize {
			return ed25519.PublicKey(data), nil
		}
	}
	if data, err := decodeBase64Any(text); err == nil && len(data) == ed25519.PublicKeySize {
		return ed25519.PublicKey(data), nil
	}
	return nil, fmt.Errorf("既不是 %d 字符 hex，也不是 base64 的 %d 字节公钥", 2*ed25519.PublicKeySize, ed25519.PublicKeySize)
}

// ParseSignature 解析签名文件内容（base64 或 hex 的 ed25519 分离签名 = 64 字节，可带 `ed25519:` 前缀）
func ParseSignature(raw []byte) ([]byte, error) {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "ed25519:"))
	if text == "" {
		return nil, errors.New("签名文件为空")
	}
	if len(text) == 2*ed25519.SignatureSize && isHex(text) {
		if data, err := hex.DecodeString(text); err == nil {
			return data, nil
		}
	}
	data, err := decodeBase64Any(text)
	if err != nil {
		return nil, fmt.Errorf("既不是 %d 字符 hex，也不是 base64（ed25519 分离签名应为 %d 字节）",
			2*ed25519.SignatureSize, ed25519.SignatureSize)
	}
	if len(data) != ed25519.SignatureSize {
		return nil, fmt.Errorf("解出 %d 字节，ed25519 签名应为 %d 字节", len(data), ed25519.SignatureSize)
	}
	return data, nil
}

// decodeBase64Any 依次尝试四种 base64 形态（带/不带填充、标准/URL 字母表）
func decodeBase64Any(text string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if data, err := encoding.DecodeString(text); err == nil {
			return data, nil
		}
	}
	return nil, errors.New("不是合法 base64")
}

// ValidateSignatureFile 签名文件名的相对路径兜底（与 manifest.checkSignature 同口径，「相对路径」）：
// 调用点自核一次——未经 manifest.Load 的调用方不得把绝对路径或 `..` 段带进 filepath.Join
// （否则 export 的读文件会把插件目录之外的文件收进包）。不校验存在性（未签名是合法形态）。
func ValidateSignatureFile(sigFile string) error {
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(sigFile)))
	if clean == "" || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("签名路径 %q 必须是插件目录内的相对路径（不得绝对、不得含 ..）", sigFile)
	}
	return nil
}

// checkSignaturePolicy 源侧签名判定（安装链的 ①verify 与 `aigo plugin verify` 共用同一实现；
// keys = 本机受信公钥清单，由调用方经 LoadTrustedKeys 取好——便于单测注入）：
//   - sigFile 不存在：EnforceSignPolicy（包源）且未给 --allow-unsigned ⇒ 拒；目录源 ⇒ 放行（无签名是常态）；
//   - sigFile 存在：签名对象（packManifest = files.sha256 的字节）缺失 ⇒ 拒（签名对象 = 清单字节）；
//   - 无受信公钥：无法验证 ⇒ 须 --allow-unsigned（signed_by 留空）；
//   - 有受信公钥：逐钥 ed25519.Verify——命中 ⇒ 过（记指纹）；全不命中 ⇒ 拒（无效签名不豁免）。
func checkSignaturePolicy(env *Env, dir, sigFile string, packManifest []byte, keys []ed25519.PublicKey, opts integrityOptions) (signatureOutcome, error) {
	if err := ValidateSignatureFile(sigFile); err != nil {
		return signatureOutcome{}, err
	}
	sigPath := filepath.Join(dir, filepath.FromSlash(sigFile))
	raw, err := os.ReadFile(sigPath)
	if errors.Is(err, fs.ErrNotExist) {
		if opts.EnforceSignPolicy && !opts.AllowUnsigned {
			return signatureOutcome{}, fmt.Errorf("包未签名：要求未签名包显式声明 --allow-unsigned" +
				"（此时 checksum 只作事后回溯，不作来源认证）")
		}
		return signatureOutcome{}, nil
	}
	if err != nil {
		return signatureOutcome{}, fmt.Errorf("读取签名 %s 失败: %w", sigFile, err)
	}
	if len(packManifest) == 0 {
		return signatureOutcome{}, fmt.Errorf("源内含签名 %s，但签名对象 %s 缺失：签名对象 = 清单文件的字节⇒ 拒装",
			sigFile, PackManifestFile)
	}
	signature, err := ParseSignature(raw)
	if err != nil {
		return signatureOutcome{}, fmt.Errorf("签名 %s 解析失败: %w", sigFile, err)
	}

	if len(keys) == 0 {
		if !opts.AllowUnsigned {
			return signatureOutcome{}, fmt.Errorf("源内含签名 %s，但本机未登记受信公钥（配置键 %s）：无法验证真伪 ⇒ "+
				"加 --allow-unsigned 明确接受**未验证**的签名，或把作者公钥填进 %s", sigFile, TrustedKeysConfigKey, TrustedKeysConfigKey)
		}
		env.Printf("⚠ 签名 %s 未验证（本机未登记 %s 受信公钥；--allow-unsigned 已显式接受未验证的签名；"+
			"signed_by 留空——不校验就不声称指纹）\n", sigFile, TrustedKeysConfigKey)
		return signatureOutcome{}, nil
	}

	for _, key := range keys {
		if ed25519.Verify(key, packManifest, signature) {
			fingerprint := Fingerprint(key)
			env.Printf("✓ 签名校验通过：%s 是 %s 字节的 ed25519 分离签名（受信公钥指纹 %s）\n",
				sigFile, PackManifestFile, fingerprint)
			return signatureOutcome{Verified: true, Fingerprint: fingerprint}, nil
		}
	}
	return signatureOutcome{}, fmt.Errorf("签名 %s 与 %d 个受信公钥（%s）均不匹配：签名无效 ⇒ 拒装"+
		"（--allow-unsigned 只覆盖「没有签名 / 无法验证」，不豁免「签名无效」——无效签名是篡改证据，fail-closed）",
		sigFile, len(keys), TrustedKeysConfigKey)
}
