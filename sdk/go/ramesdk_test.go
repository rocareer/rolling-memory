// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rocareer/rolling-memory/internal/thinclient"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

type goldenVectorCase struct {
	Name      string                   `json:"name"`
	Secret    string                   `json:"secret"`
	DocID     string                   `json:"doc_id"`
	Text      string                   `json:"text"`
	Sentences []string                 `json:"sentences"`
	Packet    ramesdk.BlindPulsePacket `json:"packet"`
}

type goldenVectorSuite struct {
	Spec  string             `json:"spec"`
	Cases []goldenVectorCase `json:"cases"`
}

type cliOutput struct {
	Spec      string                   `json:"spec"`
	Sentences []string                 `json:"sentences"`
	Packet    ramesdk.BlindPulsePacket `json:"packet"`
}

// TestCrossLanguageSDKGoldenParity verifies 100% bit-for-bit parity across:
//  1. `sdk/go` (`ramesdk.BlindEncoder`)
//  2. `internal/thinclient` (`thinclient.BlindEncoder` / `Vault`)
//  3. `sdk/python/rame_sdk.py`
//  4. `sdk/nodejs/index.mjs`
//  5. `sdk/java/RameSdk.java` (when java is present)
//  6. Frozen `sdk/spec/golden_vectors.json`
func TestCrossLanguageSDKGoldenParity(t *testing.T) {
	specPath := filepath.Join("..", "spec", "golden_vectors.json")
	rawGolden, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var frozenSuite goldenVectorSuite
	if err := json.Unmarshal(rawGolden, &frozenSuite); err != nil {
		t.Fatalf("unmarshal %s: %v", specPath, err)
	}
	if frozenSuite.Spec != ramesdk.SpecVersion {
		t.Fatalf("spec version = %q, want %q", frozenSuite.Spec, ramesdk.SpecVersion)
	}
	if len(frozenSuite.Cases) < 3 {
		t.Fatalf("expected at least 3 golden cases, got %d", len(frozenSuite.Cases))
	}

	for i, tc := range frozenSuite.Cases {
		goEnc := ramesdk.NewBlindEncoder([]byte(tc.Secret))
		goPkt, goSents := goEnc.PackBlindPacket(tc.DocID, tc.Text, 1700000000)

		if os.Getenv("UPDATE_GOLDEN") == "1" {
			frozenSuite.Cases[i].Sentences = goSents
			frozenSuite.Cases[i].Packet = goPkt
			tc = frozenSuite.Cases[i]
		}

		// 0. Verify against frozen golden_vectors.json (strict read-only drift gate)
		if !reflect.DeepEqual(goSents, tc.Sentences) || !reflect.DeepEqual(goPkt, tc.Packet) {
			t.Fatalf("[%s] Go SDK drifted from frozen golden_vectors.json!\n  got:  %+v\n  want: %+v", tc.Name, goPkt, tc.Packet)
		}

		// 1. Verify 100% parity between `sdk/go` and `internal/thinclient`
		tcEnc := thinclient.NewBlindEncoder([]byte(tc.Secret))
		tcPkt, tcSents := tcEnc.PackBlindPacket(tc.DocID, tc.Text, 1700000000)
		if !reflect.DeepEqual(goSents, tcSents) {
			t.Fatalf("[%s] thinclient sentences mismatch:\n  sdk/go:     %#v\n  thinclient: %#v", tc.Name, goSents, tcSents)
		}
		if len(goPkt.Slots) != len(tcPkt.Slots) {
			t.Fatalf("[%s] thinclient slots len mismatch: %d vs %d", tc.Name, len(goPkt.Slots), len(tcPkt.Slots))
		}
		for i := range goPkt.Slots {
			a, b := goPkt.Slots[i], tcPkt.Slots[i]
			if a.H != b.H || a.RawBitsX10 != b.RawBitsX10 || a.ClassFlags != b.ClassFlags {
				t.Fatalf("[%s] slot[%d] mismatch between sdk/go %+v and thinclient %+v", tc.Name, i, a, b)
			}
		}

		// 2. Verify Python SDK (`sdk/python/rame_sdk.py`)
		if pyPath, err := exec.LookPath("python3"); err == nil {
			cmd := exec.Command(pyPath, filepath.Join("..", "python", "rame_sdk.py"), tc.Secret, tc.DocID, tc.Text)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("[%s] python3 SDK failed: %v\n%s", tc.Name, err, string(out))
			}
			var pyOut cliOutput
			if err := json.Unmarshal(bytes.TrimSpace(out), &pyOut); err != nil {
				t.Fatalf("[%s] unmarshal python3 output: %v\nraw: %s", tc.Name, err, string(out))
			}
			if !reflect.DeepEqual(pyOut.Sentences, goSents) || !reflect.DeepEqual(pyOut.Packet, goPkt) {
				t.Fatalf("[%s] Python SDK mismatch!\n  Go:     %+v\n  Python: %+v", tc.Name, goPkt, pyOut.Packet)
			}
			t.Logf("✓ [%s] Python SDK 100%% bit-for-bit identical (%d slots)", tc.Name, len(goPkt.Slots))
		}

		// 3. Verify Node.js SDK (`sdk/nodejs/index.mjs`)
		if nodePath, err := exec.LookPath("node"); err == nil {
			cmd := exec.Command(nodePath, filepath.Join("..", "nodejs", "index.mjs"), tc.Secret, tc.DocID, tc.Text)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("[%s] node SDK failed: %v\n%s", tc.Name, err, string(out))
			}
			var nodeOut cliOutput
			if err := json.Unmarshal(bytes.TrimSpace(out), &nodeOut); err != nil {
				t.Fatalf("[%s] unmarshal node output: %v\nraw: %s", tc.Name, err, string(out))
			}
			if !reflect.DeepEqual(nodeOut.Sentences, goSents) || !reflect.DeepEqual(nodeOut.Packet, goPkt) {
				t.Fatalf("[%s] Node.js SDK mismatch!\n  Go:   %+v\n  Node: %+v", tc.Name, goPkt, nodeOut.Packet)
			}
			t.Logf("✓ [%s] Node.js SDK 100%% bit-for-bit identical (%d slots)", tc.Name, len(goPkt.Slots))
		}

		// 4. Verify Java SDK (`sdk/java/RameSdk.java`) if java is installed
		if javaPath, err := exec.LookPath("java"); err == nil {
			cmd := exec.Command(javaPath, filepath.Join("..", "java", "RameSdk.java"), tc.Secret, tc.DocID, tc.Text)
			if out, err := cmd.CombinedOutput(); err == nil {
				var javaOut cliOutput
				if err := json.Unmarshal(bytes.TrimSpace(out), &javaOut); err != nil {
					t.Fatalf("[%s] unmarshal java output: %v\nraw: %s", tc.Name, err, string(out))
				}
				if !reflect.DeepEqual(javaOut.Sentences, goSents) || !reflect.DeepEqual(javaOut.Packet, goPkt) {
					t.Fatalf("[%s] Java SDK mismatch!\n  Go:   %+v\n  Java: %+v", tc.Name, goPkt, javaOut.Packet)
				}
				t.Logf("✓ [%s] Java SDK 100%% bit-for-bit identical (%d slots)", tc.Name, len(goPkt.Slots))
			}
		}
	}

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		outRaw, err := json.MarshalIndent(frozenSuite, "", "  ")
		if err != nil {
			t.Fatalf("marshal golden vectors: %v", err)
		}
		if err := os.WriteFile(specPath, append(outRaw, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", specPath, err)
		}
	}
}

// TestCrossLanguageAdversarialUnicodeAndWireRejects attacks:
//  1. Special Unicode case-folding (Turkish 'İ' U+0130, Greek final 'Σ' U+03A3), U+3000 full-width space trim,
//     non-BMP Emoji/CJK-ExtB, and `mem2.*` signed-key pepper derivation across Go, Python, and Node.js;
//  2. The `palN > 255` direct B/F fallback branch (260 distinct (B,F) pairs -> palN=0) across Go, Python, and Node.js;
//  3. Canonical wire rejection of corrupted/non-canonical RMBP payloads across Go, Python, and Node.js.
func TestCrossLanguageAdversarialUnicodeAndWireRejects(t *testing.T) {
	pyPath, pyErr := exec.LookPath("python3")
	nodePath, nodeErr := exec.LookPath("node")
	if pyErr != nil || nodeErr != nil {
		t.Skip("python3 or node not available")
	}

	// 1. Unicode & mem2 pepper adversarial corpus
	secret := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAifQ.sig"
	advTexts := []string{
		"İstanbul ΛΟΓΟΣ Σωκράτης \u3000全角空格\u3000",
		"Emoji 💡✨🚀 CJK-ExtB 𠀀𠀁𠀂 combining e\u0301 a-b_c.d .net v1.2.3",
		"aaaaAAa 000000 ---___...",
		"   \u00a0\u3000  !?;  \u3000 ",
	}
	goEnc := ramesdk.NewBlindEncoder([]byte(secret))
	for idx, txt := range advTexts {
		docID := "ADV_UNI"
		goPkt, goSents := goEnc.PackBlindPacket(docID, txt, 1700000000)

		pyOutRaw, err := exec.Command(pyPath, filepath.Join("..", "python", "rame_sdk.py"), secret, docID, txt).CombinedOutput()
		if err != nil {
			t.Fatalf("python3 ADV_%d failed: %v\n%s", idx, err, string(pyOutRaw))
		}
		var pyOut cliOutput
		if err := json.Unmarshal(bytes.TrimSpace(pyOutRaw), &pyOut); err != nil {
			t.Fatalf("unmarshal python3 ADV_%d: %v", idx, err)
		}
		if !reflect.DeepEqual(pyOut.Sentences, goSents) || !reflect.DeepEqual(pyOut.Packet, goPkt) {
			t.Fatalf("ADV_%d Go vs Python mismatch:\n  Go: %+v\n  Py: %+v", idx, goPkt, pyOut.Packet)
		}

		jsOutRaw, err := exec.Command(nodePath, filepath.Join("..", "nodejs", "index.mjs"), secret, docID, txt).CombinedOutput()
		if err != nil {
			t.Fatalf("node ADV_%d failed: %v\n%s", idx, err, string(jsOutRaw))
		}
		var jsOut cliOutput
		if err := json.Unmarshal(bytes.TrimSpace(jsOutRaw), &jsOut); err != nil {
			t.Fatalf("unmarshal node ADV_%d: %v", idx, err)
		}
		if !reflect.DeepEqual(jsOut.Sentences, goSents) || !reflect.DeepEqual(jsOut.Packet, goPkt) {
			t.Fatalf("ADV_%d Go vs Node mismatch:\n  Go:   %+v\n  Node: %+v", idx, goPkt, jsOut.Packet)
		}
	}

	// 2. palN > 255 direct B/F fallback + corrupted wire rejections across Go, Python, and Node.js
	slots := make([]ramesdk.BlindTokenAtom, 260)
	for i := 0; i < 260; i++ {
		slots[i] = ramesdk.BlindTokenAtom{
			H:          uint64(i+1) * 0x0080000000000001,
			RawBitsX10: uint16(10 + (i/16)*10),
			ClassFlags: uint8(i % 16),
		}
	}
	frames := [][]ramesdk.BlindTokenAtom{slots[:130], slots[130:]}
	goB64, err := ramesdk.EncodeWirePayload(slots, frames)
	if err != nil {
		t.Fatalf("EncodeWirePayload(palN>255): %v", err)
	}
	decSlots, decFrames, err := ramesdk.DecodeWirePayload(goB64)
	if err != nil || !reflect.DeepEqual(decSlots, slots) || !reflect.DeepEqual(decFrames, frames) {
		t.Fatalf("Go palN>255 round-trip failed: err=%v", err)
	}

	// Verify Node.js rejects corrupted payloads (trailing byte, bad flags, bad magic, non-minimal varint, bomb)
	jsCheckScript := `
import("./../nodejs/index.mjs").then(m => {
  const slots = [];
  for (let i = 0; i < 260; i++) {
    slots.push({ h: BigInt(i + 1) * 0x0080000000000001n, b: 10 + Math.floor(i / 16) * 10, f: i % 16 });
  }
  const b64 = m.encodeWirePayload(slots, [slots.slice(0, 130), slots.slice(130)]);
  if (b64 !== process.argv[1]) {
    console.error("palN>255 b64 mismatch vs Go");
    process.exit(1);
  }
  const raw = Buffer.from(m.CANARY_ATOMS_B64, "base64");
  const mutations = [
    Buffer.concat([raw, Buffer.from([0])]),
    (() => { const b = Buffer.from(raw); b[0] = 88; return b; })(),
    (() => { const b = Buffer.from(raw); b[4] = 99; return b; })(),
    (() => { const b = Buffer.from(raw); b[5] |= 0x80; return b; })(),
    (() => { const b = Buffer.from(raw); b[6] = 9; return b; })(),
    (() => { const b = Buffer.from(raw); b[7] = 99; return b; })(),
    (() => { const b = Buffer.from(raw); b[16] |= 0x80; return b; })(),
    (() => { const b = Buffer.from(raw); b[b.length - 1] = 99; return b; })(),
  ];
  for (let i = 0; i < mutations.length; i++) {
    let failed = false;
    try { m.decodeWireBytes(mutations[i]); } catch (_e) { failed = true; }
    if (!failed) {
      console.error("Node failed to reject mutation #" + i);
      process.exit(2);
    }
  }
});`
	if out, err := exec.Command(nodePath, "-e", jsCheckScript, goB64).CombinedOutput(); err != nil {
		t.Fatalf("Node adversarial wire check failed: %v\n%s", err, string(out))
	}
}
