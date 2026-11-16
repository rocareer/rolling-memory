package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// TestSignatureFormatsAndPolicy 签名判定：文件形态（hex / base64 / ed25519: 前缀）、
// 公钥形态、以及四条 fail-closed 分支（无签名 / 无公钥 / 有效签名 / 无效签名 / 签名对象缺失）。
func TestSignatureFormatsAndPolicy(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("生成 ed25519 密钥: %v", err)
	}
	message := []byte("0f2a  plugin.yaml\n11bb  plugin.go\n")
	signature := ed25519.Sign(priv, message)

	// 签名文件形态：base64（带换行）/ hex / `ed25519:` 前缀
	for _, content := range []string{
		base64.StdEncoding.EncodeToString(signature) + "\n",
		hex.EncodeToString(signature),
		"ed25519:" + base64.RawStdEncoding.EncodeToString(signature),
	} {
		parsed, err := ParseSignature([]byte(content))
		if err != nil {
			t.Errorf("签名 %q 应可解析: %v", content, err)
			continue
		}
		if !bytes.Equal(parsed, signature) {
			t.Errorf("签名 %q 解出的字节与原始不符", content)
		}
	}
	if _, err := ParseSignature([]byte("not-a-signature\n")); err == nil {
		t.Error("非 hex / base64 的签名应拒")
	}
	if _, err := ParseSignature([]byte(base64.StdEncoding.EncodeToString([]byte("short")))); err == nil {
		t.Error("长度不是 64 字节的签名应拒")
	}

	// 公钥形态：base64 / hex / `ed25519:` 前缀；指纹形态不可当公钥（无法验签）
	for _, content := range []string{
		base64.StdEncoding.EncodeToString(pub),
		hex.EncodeToString(pub),
		"ed25519:" + base64.StdEncoding.EncodeToString(pub),
	} {
		parsed, err := ParsePublicKey(content)
		if err != nil {
			t.Errorf("公钥 %q 应可解析: %v", content, err)
			continue
		}
		if !bytes.Equal(parsed, pub) {
			t.Errorf("公钥 %q 解出的字节与原始不符", content)
		}
	}
	if _, err := ParsePublicKey(Fingerprint(pub)); err == nil {
		t.Error("指纹（sha256:…）不能当公钥——必须填公钥本身（指纹无法验签）")
	}
	if !strings.HasPrefix(Fingerprint(pub), "sha256:") {
		t.Errorf("指纹形态应为 sha256:<hex>: %q", Fingerprint(pub))
	}

	dir := t.TempDir()
	env := &Env{Root: dir, Out: &bytes.Buffer{}}
	sigPath := filepath.Join(dir, DefaultSignatureFile)
	keys := []ed25519.PublicKey{pub}

	// ① 有效签名 + 受信公钥 ⇒ 过，且记指纹
	mustWrite(t, sigPath, base64.StdEncoding.EncodeToString(signature))
	outcome, err := checkSignaturePolicy(env, dir, DefaultSignatureFile, message, keys, integrityOptions{EnforceSignPolicy: true})
	if err != nil {
		t.Fatalf("有效签名应通过: %v", err)
	}
	if !outcome.Verified || outcome.Fingerprint != Fingerprint(pub) {
		t.Errorf("有效签名应记指纹 %q，实际 %+v", Fingerprint(pub), outcome)
	}

	// ② 无受信公钥 ⇒ 须 --allow-unsigned（未验证不声称指纹）
	if _, err := checkSignaturePolicy(env, dir, DefaultSignatureFile, message, nil, integrityOptions{}); err == nil {
		t.Error("有签名但无受信公钥时应拒（须 --allow-unsigned）")
	}
	outcome, err = checkSignaturePolicy(env, dir, DefaultSignatureFile, message, nil, integrityOptions{AllowUnsigned: true})
	if err != nil {
		t.Fatalf("--allow-unsigned 应放行未验证的签名: %v", err)
	}
	if outcome.Verified || outcome.Fingerprint != "" {
		t.Errorf("未验证的签名不得声称指纹: %+v", outcome)
	}

	// ③ 无效签名 ⇒ 一律拒（--allow-unsigned 不豁免）
	mustWrite(t, sigPath, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("tampered"))))
	if _, err := checkSignaturePolicy(env, dir, DefaultSignatureFile, message, keys, integrityOptions{AllowUnsigned: true}); err == nil {
		t.Error("无效签名应拒——--allow-unsigned 只覆盖「没有签名 / 无法验证」，不豁免「签名无效」")
	}

	// ④ 签名对象（files.sha256 的字节）缺失 ⇒ 拒
	if _, err := checkSignaturePolicy(env, dir, DefaultSignatureFile, nil, keys, integrityOptions{AllowUnsigned: true}); err == nil {
		t.Error("签名对象缺失应拒（签名对象 = 清单文件的字节）")
	}

	// ⑤ 无签名：包源须 --allow-unsigned；目录源放行
	if err := os.Remove(sigPath); err != nil {
		t.Fatal(err)
	}
	if _, err := checkSignaturePolicy(env, dir, DefaultSignatureFile, nil, keys, integrityOptions{EnforceSignPolicy: true}); err == nil {
		t.Error("包源未签名时应拒（须 --allow-unsigned）")
	}
	if _, err := checkSignaturePolicy(env, dir, DefaultSignatureFile, nil, keys, integrityOptions{}); err != nil {
		t.Errorf("目录源无签名是常态，应放行: %v", err)
	}
}

// TestSignaturePathValidation 签名的相对路径约束（「signature = 相对路径」）：
// 两层都拦——manifest 校验（checkSignature，插件方声明入口）与调用点兜底（ValidateSignatureFile）。
// 反例是「插件目录之外的文件被当签名读进包」这一形态（export 拼路径打包）。
func TestSignaturePathValidation(t *testing.T) {
	for _, bad := range []string{"../outside.sig", "/tmp/outside.sig", "a/../../outside.sig", "", "  "} {
		if err := ValidateSignatureFile(bad); err == nil {
			t.Errorf("签名路径 %q 应被拒（不得绝对、不得含 ..）", bad)
		}
	}
	for _, ok := range []string{"plugin.yaml.sig", "sig/plugin.yaml.sig", "a.b.sig"} {
		if err := ValidateSignatureFile(ok); err != nil {
			t.Errorf("签名路径 %q 应放行: %v", ok, err)
		}
	}

	base := "name: demo\ntitle: 示例\nversion: 1.0.0\nkind: code\nhost_api: \">=1 <2\"\n"
	for _, bad := range []string{"../outside.sig", "a/../../outside.sig", "files.sha256", "plugin.yaml"} {
		if _, _, err := manifest.Parse([]byte(base+"signature: "+bad+"\n"), manifest.Options{}); err == nil {
			t.Errorf("manifest 的 signature: %s 应被拒（相对路径约束 / 不得占用包内核定名）", bad)
		}
	}
	if _, _, err := manifest.Parse([]byte(base+"signature: plugin.yaml.sig\n"), manifest.Options{}); err != nil {
		t.Errorf("合法的 relative signature 路径应通过: %v", err)
	}
}

// TestVerifyPackageSignaturePolicy 包通道：未签名包须显式 --allow-unsigned；
// 包内 files.sha256 由 export 生成并逐项比对（含清单之外夹带的拒绝）。
func TestVerifyPackageSignaturePolicy(t *testing.T) {
	root := t.TempDir()
	for _, lang := range []string{"zh-cn", "en"} {
		if err := os.MkdirAll(filepath.Join(root, "web", "src", "lang", lang), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := &Env{Root: root, Out: &bytes.Buffer{}}
	if err := Scaffold(env, "demo", ScaffoldOptions{Kind: "code", Title: "签名用例"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	target, err := Export(env, "demo", "")
	if err != nil {
		t.Fatalf("打包（含出包自校验）应成功: %v", err)
	}

	ctx := context.Background()
	// 未签名包：不给 --allow-unsigned ⇒ 拒
	if _, err := VerifyPackage(ctx, env, target, false); err == nil {
		t.Error("未签名包在未给 --allow-unsigned 时应拒")
	} else if !strings.Contains(err.Error(), "--allow-unsigned") {
		t.Errorf("拒绝理由应点名 --allow-unsigned: %v", err)
	}
	// 给了 --allow-unsigned ⇒ 过，且不得声称指纹
	report, err := VerifyPackage(ctx, env, target, true)
	if err != nil {
		t.Fatalf("--allow-unsigned 的包应通过: %v", err)
	}
	if report.Signature.Verified || report.Signature.Fingerprint != "" {
		t.Errorf("未签名包不得声称指纹: %+v", report.Signature)
	}
	if report.Name != "demo" || report.Files == 0 || !report.RequiresChecked {
		t.Errorf("包读数不完整: %+v", report)
	}
}
