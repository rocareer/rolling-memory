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

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestExtractStructuredDateHarmonics_ZeroLexiconStateMachine(t *testing.T) {
	positives := []struct {
		input string
		want  []string
	}{
		{"Deploy scheduled on 2026/11/8 and verified on 2026.11.09!", []string{"2026-11-08", "2026-11-09"}},
		{"架构定版于2026年11月8日，复盘于2026年12月01日。", []string{"2026-11-08", "2026-12-01"}},
		{"Already canonical 2026-11-08.", []string{"2026-11-08"}},
	}
	for _, tc := range positives {
		got := ramesdk.ExtractStructuredDateHarmonics(tc.input)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("ExtractStructuredDateHarmonics(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}

	negatives := []string{
		"RollingMemory v1.0.1 and v2026.15.1",
		"Host 1920.10.10.1 or 10.0.0.1",
		"Out of range 2026-13-01 or 2026-02-35 or 1899-12-31",
		"Spaced numbers 2026 11 08 are not joined dates",
	}
	for _, neg := range negatives {
		if got := ramesdk.ExtractStructuredDateHarmonics(neg); len(got) != 0 {
			t.Fatalf("negative case %q produced unexpected dates: %v", neg, got)
		}
	}
}

func TestCanonicalHarmonicOverlay_WireRoundtripAndIdempotence(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("harmonic-test-secret-2026"))
	enc.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{
		EnableStructuredDates: true,
		Expander: func(tok string) []string {
			switch tok {
			case "kg", "千克", "公斤":
				return []string{"@unit:mass:kg", "@dim:mass"}
			default:
				return nil
			}
		},
	})

	pkt1, _ := enc.PackBlindPacket("U1", "2026/11/8 load 50 kg", 1700000000)
	pkt2, _ := enc.PackBlindPacket("U2", "2026年11月8日 载重 50 千克", 1700000000)

	if pkt1.AtomsB64 == "" || pkt2.AtomsB64 == "" {
		t.Fatal("expected non-empty RMBP atoms_b64 payload")
	}
	decSlots, decFrames, err := ramesdk.DecodeWirePayload(pkt1.AtomsB64)
	if err != nil {
		t.Fatalf("DecodeWirePayload failed: %v", err)
	}
	if !reflect.DeepEqual(decSlots, pkt1.Slots) || !reflect.DeepEqual(decFrames, pkt1.SentenceFrames) {
		t.Fatalf("RMBP wire roundtrip mismatch")
	}

	// Verify that both packets share the exact same canonical ISO date hash and canonical mass unit harmonic hash
	dateH := enc.KeyedHash64("2026-11-08")
	unitH := enc.KeyedHash64("@unit:mass:kg")
	hasHash := func(slots []ramesdk.BlindTokenAtom, target uint64) bool {
		for _, s := range slots {
			if s.H == target {
				return true
			}
		}
		return false
	}
	if !hasHash(pkt1.Slots, dateH) || !hasHash(pkt2.Slots, dateH) {
		t.Fatalf("expected both packets to contain canonical date beacon H(2026-11-08)")
	}
	if !hasHash(pkt1.Slots, unitH) || !hasHash(pkt2.Slots, unitH) {
		t.Fatalf("expected both packets to contain canonical unit harmonic H(@unit:mass:kg)")
	}
}

func TestF19F_CrossLanguageCanonicalHarmonicBitExactParity(t *testing.T) {
	secret := "f19f-cross-lang-harmonic-key"
	docID := "DOC_HARM_01"
	text := "2026/11/8 NYC cargo weight 50 kg. 2026年11月9日 复核 50 千克!"

	goEnc := ramesdk.NewBlindEncoder([]byte(secret))
	goEnc.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{
		EnableStructuredDates: true,
		Expander: func(tok string) []string {
			switch tok {
			case "kg", "千克":
				return []string{"@unit:mass:kg", "@dim:mass"}
			case "nyc":
				return []string{"@geo:nyc"}
			default:
				return nil
			}
		},
	})
	goPkt, _ := goEnc.PackBlindPacket(docID, text, 1700000000)

	// 1. Python bit-for-bit parity
	if pyPath, err := exec.LookPath("python3"); err == nil {
		pyCode := `import json, sys; sys.path.insert(0, "../python"); import rame_sdk
enc = rame_sdk.BlindEncoder(sys.argv[1].encode("utf-8"))
def exp_fn(tok):
    if tok in ("kg", "千克"): return ["@unit:mass:kg", "@dim:mass"]
    if tok == "nyc": return ["@geo:nyc"]
    return []
enc.set_canonical_harmonics(enable_structured_dates=True, expander=exp_fn)
pkt, _ = enc.pack_blind_packet(sys.argv[2], sys.argv[3], 1700000000)
print(json.dumps(pkt))`
		out, err := exec.Command(pyPath, "-c", pyCode, secret, docID, text).CombinedOutput()
		if err != nil {
			t.Fatalf("python harmonic run failed: %v\n%s", err, string(out))
		}
		var pyPkt ramesdk.BlindPulsePacket
		if err := json.Unmarshal(bytes.TrimSpace(out), &pyPkt); err != nil {
			t.Fatalf("unmarshal python pkt: %v\n%s", err, string(out))
		}
		if pyPkt.AtomsB64 != goPkt.AtomsB64 || !reflect.DeepEqual(pyPkt.Slots, goPkt.Slots) {
			t.Fatalf("Python canonical harmonic drift!\nGo: %s\nPy: %s", goPkt.AtomsB64, pyPkt.AtomsB64)
		}
	}

	// 2. Node.js bit-for-bit parity
	if nodePath, err := exec.LookPath("node"); err == nil {
		nodeCode := `import { BlindEncoder } from "../nodejs/index.mjs";
const enc = new BlindEncoder(process.argv[1]);
enc.setCanonicalHarmonics({
  enableStructuredDates: true,
  expander: (tok) => {
    if (tok === "kg" || tok === "千克") return ["@unit:mass:kg", "@dim:mass"];
    if (tok === "nyc") return ["@geo:nyc"];
    return [];
  }
});
const { packet } = enc.packBlindPacket(process.argv[2], process.argv[3], 1700000000);
console.log(packet.atoms_b64);`
		out, err := exec.Command(nodePath, "--input-type=module", "-e", nodeCode, secret, docID, text).CombinedOutput()
		if err != nil {
			t.Fatalf("node harmonic run failed: %v\n%s", err, string(out))
		}
		if gotB64 := string(bytes.TrimSpace(out)); gotB64 != goPkt.AtomsB64 {
			t.Fatalf("Node.js canonical harmonic atoms_b64 drift!\nGo:   %s\nNode: %s", goPkt.AtomsB64, gotB64)
		}
	}

	// 3. Java bit-for-bit parity (when a working JDK runtime is installed)
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
			driverSrc := `import java.nio.charset.StandardCharsets;
import java.util.List;
public class HarmonicDriver {
    public static void main(String[] args) {
        RameSdk.BlindEncoder enc = new RameSdk.BlindEncoder(args[0].getBytes(StandardCharsets.UTF_8));
        enc.setCanonicalHarmonics(true, tok -> {
            if ("kg".equals(tok) || "千克".equals(tok)) return List.of("@unit:mass:kg", "@dim:mass");
            if ("nyc".equals(tok)) return List.of("@geo:nyc");
            return List.of();
        });
        RameSdk.BlindPulsePacket pkt = enc.packBlindPacket(args[1], args[2], 1700000000L);
        System.out.println(pkt.atomsB64);
    }
}`
			if err := os.WriteFile(filepath.Join(tmpDir, "HarmonicDriver.java"), []byte(driverSrc), 0600); err != nil {
				t.Fatalf("write HarmonicDriver.java: %v", err)
			}
			if out, err := exec.Command(javacPath, "-encoding", "UTF-8", filepath.Join(tmpDir, "RameSdk.java"), filepath.Join(tmpDir, "HarmonicDriver.java")).CombinedOutput(); err != nil {
				t.Fatalf("javac failed: %v\n%s", err, string(out))
			}
			out, err := exec.Command(javaPath, "-cp", tmpDir, "HarmonicDriver", secret, docID, text).CombinedOutput()
			if err != nil {
				t.Fatalf("java HarmonicDriver failed: %v\n%s", err, string(out))
			}
			if gotB64 := string(bytes.TrimSpace(out)); gotB64 != goPkt.AtomsB64 {
				t.Fatalf("Java canonical harmonic atoms_b64 drift!\nGo:   %s\nJava: %s", goPkt.AtomsB64, gotB64)
			}
		}
	}
}

func TestCrossLanguageInlineSymbolAndSEAsianUnitHarmonicParity(t *testing.T) {
	if got := ramesdk.ExtractInlineSymbolUnits("Paid $480 at 95% efficiency and 25°C."); !reflect.DeepEqual(got, []string{"usd", "pct", "degc"}) {
		t.Fatalf("ExtractInlineSymbolUnits = %v, want [usd pct degc]", got)
	}
	if got := ramesdk.ExtractSEAsianUnitWords("ซื้อของหนัก5กิโลกรัมใช้เวลา30นาทีลดราคา20เปอร์เซ็นต์"); !reflect.DeepEqual(got, []string{"กิโลกรัม", "นาที", "เปอร์เซ็นต์"}) {
		t.Fatalf("ExtractSEAsianUnitWords = %v, want [กิโลกรัม นาที เปอร์เซ็นต์]", got)
	}

	secret := "inline-and-sea-unit-harmonic-key"
	docID := "DOC_UNIT_SCAN_01"
	text := "Paid $480 at 95% and 25°C. ซื้อของหนัก5กิโลกรัมใช้เวลา30นาทีลดราคา20เปอร์เซ็นต์!"

	goEnc := ramesdk.NewBlindEncoder([]byte(secret))
	goEnc.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{
		EnableStructuredDates: true,
		Expander:              ramesdk.LookupClosedSetHarmonics,
	})
	goPkt, _ := goEnc.PackBlindPacket(docID, text, 1700000000)

	seen := make(map[uint64]bool, len(goPkt.Slots))
	for _, s := range goPkt.Slots {
		seen[s.H] = true
	}
	for _, wantTok := range []string{
		"usd", "@unit:curr:usd", "@dim:money",
		"pct", "@unit:stat:pct", "@dim:stat",
		"degc", "@unit:temp:deg", "@dim:temp",
		"กิโลกรัม", "@unit:mass:kg", "@dim:mass",
		"นาที", "@unit:time:min", "@dim:duration",
		"เปอร์เซ็นต์",
	} {
		if !seen[goEnc.KeyedHash64(wantTok)] {
			t.Fatalf("expected canonical harmonic pulse to contain %q", wantTok)
		}
	}

	if pyPath, err := exec.LookPath("python3"); err == nil {
		pyCode := `import json, sys; sys.path.insert(0, "../python"); import rame_sdk
assert rame_sdk.extract_inline_symbol_units("Paid $480 at 95% and 25°C.") == ["usd", "pct", "degc"]
assert rame_sdk.extract_se_asian_unit_words("ซื้อของหนัก5กิโลกรัมใช้เวลา30นาทีลดราคา20เปอร์เซ็นต์") == ["กิโลกรัม", "นาที", "เปอร์เซ็นต์"]
enc = rame_sdk.BlindEncoder(sys.argv[1].encode("utf-8"))
enc.set_canonical_harmonics(enable_structured_dates=True, expander=rame_sdk.lookup_closed_set_harmonics)
pkt, _ = enc.pack_blind_packet(sys.argv[2], sys.argv[3], 1700000000)
print(json.dumps(pkt))`
		out, err := exec.Command(pyPath, "-c", pyCode, secret, docID, text).CombinedOutput()
		if err != nil {
			t.Fatalf("python unit harmonic run failed: %v\n%s", err, string(out))
		}
		var pyPkt ramesdk.BlindPulsePacket
		if err := json.Unmarshal(bytes.TrimSpace(out), &pyPkt); err != nil {
			t.Fatalf("unmarshal python pkt: %v\n%s", err, string(out))
		}
		if pyPkt.AtomsB64 != goPkt.AtomsB64 || !reflect.DeepEqual(pyPkt.Slots, goPkt.Slots) {
			t.Fatalf("Python unit harmonic drift!\nGo: %s\nPy: %s", goPkt.AtomsB64, pyPkt.AtomsB64)
		}
	}

	if nodePath, err := exec.LookPath("node"); err == nil {
		nodeCode := `import { BlindEncoder, lookupClosedSetHarmonics, extractInlineSymbolUnits, extractSEAsianUnitWords } from "../nodejs/index.mjs";
if (JSON.stringify(extractInlineSymbolUnits("Paid $480 at 95% and 25°C.")) !== JSON.stringify(["usd", "pct", "degc"])) process.exit(2);
if (JSON.stringify(extractSEAsianUnitWords("ซื้อของหนัก5กิโลกรัมใช้เวลา30นาทีลดราคา20เปอร์เซ็นต์")) !== JSON.stringify(["กิโลกรัม", "นาที", "เปอร์เซ็นต์"])) process.exit(3);
const enc = new BlindEncoder(process.argv[1]);
enc.setCanonicalHarmonics({ enableStructuredDates: true, expander: lookupClosedSetHarmonics });
const { packet } = enc.packBlindPacket(process.argv[2], process.argv[3], 1700000000);
console.log(packet.atoms_b64);`
		out, err := exec.Command(nodePath, "--input-type=module", "-e", nodeCode, secret, docID, text).CombinedOutput()
		if err != nil {
			t.Fatalf("node unit harmonic run failed: %v\n%s", err, string(out))
		}
		if gotB64 := string(bytes.TrimSpace(out)); gotB64 != goPkt.AtomsB64 {
			t.Fatalf("Node.js unit harmonic atoms_b64 drift!\nGo:   %s\nNode: %s", goPkt.AtomsB64, gotB64)
		}
	}

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
			driverSrc := `import java.nio.charset.StandardCharsets;
import java.util.List;
public class UnitHarmonicDriver {
    public static void main(String[] args) {
        if (!RameSdk.extractInlineSymbolUnits("Paid $480 at 95% and 25°C.").equals(List.of("usd", "pct", "degc"))) {
            throw new IllegalStateException("extractInlineSymbolUnits mismatch");
        }
        if (!RameSdk.extractSEAsianUnitWords("ซื้อของหนัก5กิโลกรัมใช้เวลา30นาทีลดราคา20เปอร์เซ็นต์").equals(List.of("กิโลกรัม", "นาที", "เปอร์เซ็นต์"))) {
            throw new IllegalStateException("extractSEAsianUnitWords mismatch");
        }
        RameSdk.BlindEncoder enc = new RameSdk.BlindEncoder(args[0].getBytes(StandardCharsets.UTF_8));
        enc.setCanonicalHarmonics(true, RameSdk::lookupClosedSetHarmonics);
        RameSdk.BlindPulsePacket pkt = enc.packBlindPacket(args[1], args[2], 1700000000L);
        System.out.println(pkt.atomsB64);
    }
}`
			if err := os.WriteFile(filepath.Join(tmpDir, "UnitHarmonicDriver.java"), []byte(driverSrc), 0600); err != nil {
				t.Fatalf("write UnitHarmonicDriver.java: %v", err)
			}
			if out, err := exec.Command(javacPath, "-encoding", "UTF-8", filepath.Join(tmpDir, "RameSdk.java"), filepath.Join(tmpDir, "UnitHarmonicDriver.java")).CombinedOutput(); err != nil {
				t.Fatalf("javac failed: %v\n%s", err, string(out))
			}
			out, err := exec.Command(javaPath, "-cp", tmpDir, "UnitHarmonicDriver", secret, docID, text).CombinedOutput()
			if err != nil {
				t.Fatalf("java UnitHarmonicDriver failed: %v\n%s", err, string(out))
			}
			if gotB64 := string(bytes.TrimSpace(out)); gotB64 != goPkt.AtomsB64 {
				t.Fatalf("Java unit harmonic atoms_b64 drift!\nGo:   %s\nJava: %s", goPkt.AtomsB64, gotB64)
			}
		}
	}
}
