// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// wideFProbeVectors 冻结两组探针载荷，覆盖 RMBP v1 `FlagWideF`（8-bit F 列）的全部编码分支：
//   - "palette-wide"：12 槽 / 6 个唯一 (B,F) 组合 ⇒ 调色板多进制打包分支，且 F 跨越旧 4-bit 天花板
//     （0x00 / 0x0F 边界 + 0x10 谐波声明位 + 0x18 形态组合 + 0x95 / 0xFF 预留高位极值）；
//   - "dict-wide"：300 槽全唯一 (B,F) ⇒ 唯一组合数 > 255 强制关闭调色板、走字典直写列分支，
//     且全部 F ∈ 0x10..0x1F 使宽 F 列在两个分支上都成为必由之路。
func wideFProbeVectors() map[string][]ramesdk.BlindTokenAtom {
	palette := []ramesdk.BlindTokenAtom{
		{H: 0x10, RawBitsX10: 10, ClassFlags: 0x00},
		{H: 0x20, RawBitsX10: 10, ClassFlags: 0x0F},
		{H: 0x30, RawBitsX10: 120, ClassFlags: 0x10},
		{H: 0x40, RawBitsX10: 120, ClassFlags: 0x18},
		{H: 0x50, RawBitsX10: 200, ClassFlags: 0x95},
		{H: 0x60, RawBitsX10: 200, ClassFlags: 0xFF},
	}
	var paletteWide []ramesdk.BlindTokenAtom
	for i := 0; i < 12; i++ {
		paletteWide = append(paletteWide, palette[i%len(palette)])
	}
	var dictWide []ramesdk.BlindTokenAtom
	for i := 0; i < 300; i++ {
		dictWide = append(dictWide, ramesdk.BlindTokenAtom{
			H:          uint64(0x1000 + i),
			RawBitsX10: uint16(10 + i),
			ClassFlags: uint8(0x10 + i%16),
		})
	}
	return map[string][]ramesdk.BlindTokenAtom{
		"palette-wide": paletteWide,
		"dict-wide":    dictWide,
	}
}

func wideFCanonical(slots []ramesdk.BlindTokenAtom) string {
	var sb strings.Builder
	for i, a := range slots {
		if i > 0 {
			sb.WriteByte('|')
		}
		sb.WriteString(strconv.FormatUint(a.H, 10))
		sb.WriteByte(':')
		sb.WriteString(strconv.Itoa(int(a.RawBitsX10)))
		sb.WriteByte(':')
		sb.WriteString(strconv.Itoa(int(a.ClassFlags)))
	}
	return sb.String()
}

// TestWideFCrossLanguageBitExactParity 钉死 8-bit `ClassFlags`（0x10..0xFF 预留域）在四语言
// 编解码器上的逐位一致：Go 产出基载荷 → 每种语言解码为原子三元组并原样重编码 → 重编码字节必须
// 与 Go 基载荷逐字节相同、解码三元组必须与 Go 解码逐位相同。任何语言若把 F 静默掩回 4-bit、
// 或对宽 F 头校验/位流处理有偏差，双向差分都会立即变红。
func TestWideFCrossLanguageBitExactParity(t *testing.T) {
	vectors := wideFProbeVectors()
	names := []string{"palette-wide", "dict-wide"}

	type ref struct {
		b64       string
		canonical string
	}
	goRef := make(map[string]ref, len(vectors))
	for _, name := range names {
		slots := vectors[name]
		b64, err := ramesdk.EncodeWirePayload(slots, [][]ramesdk.BlindTokenAtom{slots})
		if err != nil {
			t.Fatalf("%s: EncodeWirePayload failed: %v", name, err)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("%s: decode base64: %v", name, err)
		}
		if raw[5]&0x02 == 0 {
			t.Fatalf("%s: 探针载荷必须置 FlagWideF（header flags=%#x），否则本测试未覆盖宽 F 分支", name, raw[5])
		}
		decSlots, decFrames, err := ramesdk.DecodeWirePayload(b64)
		if err != nil {
			t.Fatalf("%s: DecodeWirePayload failed: %v", name, err)
		}
		high := false
		for _, a := range decSlots {
			if a.ClassFlags > 0x0F {
				high = true
			}
		}
		if !high {
			t.Fatalf("%s: 探针向量解码后必须含 F > 0x0F 的原子", name)
		}
		reB64, err := ramesdk.EncodeWirePayload(decSlots, decFrames)
		if err != nil {
			t.Fatalf("%s: Go 重编码失败: %v", name, err)
		}
		if reB64 != b64 {
			t.Fatalf("%s: Go 解码→重编码不收敛\nwant %s\ngot  %s", name, b64, reB64)
		}
		goRef[name] = ref{b64: b64, canonical: wideFCanonical(decSlots)}
	}
	args := make([]string, 0, len(names))
	for _, name := range names {
		args = append(args, name+"="+goRef[name].b64)
	}

	verify := func(lang string, out []byte) {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != len(names) {
			t.Fatalf("%s 驱动输出行数 %d != 向量数 %d\n%s", lang, len(lines), len(names), string(out))
		}
		for _, line := range lines {
			parts := strings.Split(strings.TrimSpace(line), "\t")
			if len(parts) != 3 {
				t.Fatalf("%s 驱动输出格式异常: %q", lang, line)
			}
			want, ok := goRef[parts[0]]
			if !ok {
				t.Fatalf("%s 驱动返回未知向量 %q", lang, parts[0])
			}
			if parts[1] != want.b64 {
				t.Fatalf("%s/%s 重编码字节漂移\nGo:   %s\n%s: %s", lang, parts[0], want.b64, lang, parts[1])
			}
			if parts[2] != want.canonical {
				t.Fatalf("%s/%s 解码三元组漂移\nGo:   %s\n%s: %s", lang, parts[0], want.canonical, lang, parts[2])
			}
		}
	}

	// 1. Python
	if pyPath, err := exec.LookPath("python3"); err == nil {
		pyCode := `import sys; sys.path.insert(0, "../python"); import rame_sdk
def canon(slots):
    return "|".join("%d:%d:%d" % (int(a["h"]), int(a["b"]), int(a["f"])) for a in slots)
for arg in sys.argv[1:]:
    name, _, b64 = arg.partition("=")
    slots, frames = rame_sdk.decode_wire_payload(b64)
    print("%s\t%s\t%s" % (name, rame_sdk.encode_wire_payload(slots, frames), canon(slots)))`
		out, err := exec.Command(pyPath, append([]string{"-c", pyCode}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("python wide-F 驱动失败: %v\n%s", err, string(out))
		}
		verify("python", out)
	} else {
		t.Logf("python3 缺席：跳过 Python 宽 F 对拍")
	}

	// 2. Node.js
	if nodePath, err := exec.LookPath("node"); err == nil {
		nodeCode := `import { decodeWirePayload, encodeWirePayload } from "../nodejs/index.mjs";
const canon = (slots) => slots.map((a) => a.h + ":" + a.b + ":" + a.f).join("|");
for (const arg of process.argv.slice(1)) {
  const eq = arg.indexOf("=");
  if (eq <= 0) continue;
  const name = arg.slice(0, eq);
  const { slots, frames } = decodeWirePayload(arg.slice(eq + 1));
  console.log(name + "\t" + encodeWirePayload(slots, frames) + "\t" + canon(slots));
}`
		out, err := exec.Command(nodePath, append([]string{"--input-type=module", "-e", nodeCode}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("node wide-F 驱动失败: %v\n%s", err, string(out))
		}
		verify("node", out)
	} else {
		t.Logf("node 缺席：跳过 Node.js 宽 F 对拍")
	}

	// 3. Java（需可用 JDK；缺省时显式登记跳过，不静默通过）
	if javacPath, err := exec.LookPath("javac"); err == nil {
		if javaPath, err2 := exec.LookPath("java"); err2 == nil && exec.Command(javacPath, "-version").Run() == nil {
			tmpDir := t.TempDir()
			javaSrc, err := os.ReadFile(filepath.Join("..", "java", "RameSdk.java"))
			if err != nil {
				t.Fatalf("read RameSdk.java: %v", err)
			}
			if err := os.WriteFile(filepath.Join(tmpDir, "RameSdk.java"), javaSrc, 0600); err != nil {
				t.Fatalf("write RameSdk.java: %v", err)
			}
			driverSrc := `import java.util.List;
public class WideFDriver {
    public static void main(String[] args) {
        for (String arg : args) {
            int eq = arg.indexOf('=');
            String name = arg.substring(0, eq);
            List<RameSdk.BlindTokenAtom> slots = RameSdk.decodeWirePayload(arg.substring(eq + 1));
            String re = RameSdk.encodeWirePayload(slots, List.of(slots));
            StringBuilder sb = new StringBuilder();
            for (RameSdk.BlindTokenAtom a : slots) {
                if (sb.length() > 0) sb.append('|');
                sb.append(Long.toUnsignedString(a.h)).append(':').append(a.b).append(':').append(a.f);
            }
            System.out.println(name + "\t" + re + "\t" + sb);
        }
    }
}`
			if err := os.WriteFile(filepath.Join(tmpDir, "WideFDriver.java"), []byte(driverSrc), 0600); err != nil {
				t.Fatalf("write WideFDriver.java: %v", err)
			}
			if out, err := exec.Command(javacPath, "-encoding", "UTF-8", filepath.Join(tmpDir, "RameSdk.java"), filepath.Join(tmpDir, "WideFDriver.java")).CombinedOutput(); err != nil {
				t.Fatalf("javac failed: %v\n%s", err, string(out))
			}
			out, err := exec.Command(javaPath, append([]string{"-cp", tmpDir, "WideFDriver"}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("java wide-F 驱动失败: %v\n%s", err, string(out))
			}
			verify("java", bytes.TrimSpace(out))
		} else {
			t.Logf("java 运行时缺席：跳过 Java 宽 F 对拍")
		}
	} else {
		t.Logf("javac 缺席：跳过 Java 宽 F 对拍")
	}
}
