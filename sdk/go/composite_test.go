// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// compositeProbeResult is the language-neutral shape emitted by every composite
// probe (Go uses the SDK packet JSON directly; Python/Node/Java probes print the
// same keys).
type compositeProbeResult struct {
	Packets     []ramesdk.BlindPulsePacket `json:"packets"`
	SubToParent map[string]string          `json:"sub_to_parent"`
}

// compositeProbeFixture is shared verbatim with the three generated probes so
// any encoder drift shows up as a value mismatch instead of a probe error.
func compositeProbeFixture() (string, string, string, int64, []ramesdk.SubUnit) {
	secret := "rame-composite-parity-key-v1"
	parentID := "docA#L1-10"
	parentText := "Header memory body with groceries summary."
	occurredAt := int64(1700000000)
	subUnits := []ramesdk.SubUnit{
		{Kind: "detail", ID: "docA#L1-10#detail:0", BlockID: "docA#L1-10#detail:0", Text: "Detail first block.", FileParse: "Detail first block parsed.", LineStart: 11, LineEnd: 12},
		{Kind: "detail", FileParse: "Second parsed block only.", LineStart: 13, LineEnd: 13},
		{Kind: "header", Text: "Header block text.", LineStart: 1, LineEnd: 2},
		{Kind: "empty", Text: "   "},
	}
	return secret, parentID, parentText, occurredAt, subUnits
}

// TestCrossLanguageCompositePacketParity verifies the 4D composite encoder
// (`PackCompositePackets` / `pack_composite_packets` / `packCompositePackets`)
// is a 1:1 runtime capability across Go, Python, Node.js and Java: parent and
// sub-unit packets, spatial commitments, cited parent ids and the sub-id →
// parent-id map must match bit-for-bit.
func TestCrossLanguageCompositePacketParity(t *testing.T) {
	secret, parentID, parentText, occurredAt, subUnits := compositeProbeFixture()
	enc := ramesdk.NewBlindEncoder([]byte(secret))
	packets, subToParent := enc.PackCompositePackets(parentID, parentText, occurredAt, subUnits)
	want := compositeProbeResult{Packets: packets, SubToParent: subToParent}
	// 1 parent + 3 non-empty sub-units ("empty" is skipped).
	if len(want.Packets) != 4 {
		t.Fatalf("Go composite fixture produced %d packets, want 4: %+v", len(want.Packets), want.Packets)
	}
	for _, pkt := range want.Packets[1:] {
		if pkt.DocHash != want.Packets[0].DocHash {
			t.Fatalf("sub-unit %q doc_h=%d does not match parent doc_h=%d", pkt.UnitID, pkt.DocHash, want.Packets[0].DocHash)
		}
		if len(pkt.CitedUnitIDs) != 1 || pkt.CitedUnitIDs[0] != parentID {
			t.Fatalf("sub-unit %q cited_unit_ids=%v, want [%s]", pkt.UnitID, pkt.CitedUnitIDs, parentID)
		}
	}
	if want.SubToParent["docA#L1-10#detail:0"] != parentID || want.SubToParent["docA#L1-10#detail:1"] != parentID {
		t.Fatalf("sub_to_parent map drifted: %v", want.SubToParent)
	}

	dir := t.TempDir()
	secretLit := strconv.Quote(secret)
	parentIDLit := strconv.Quote(parentID)
	parentTextLit := strconv.Quote(parentText)

	if python, err := exec.LookPath("python3"); err == nil {
		sdkDir, absErr := filepath.Abs(filepath.Join("..", "python"))
		if absErr != nil {
			t.Fatalf("resolve python sdk dir: %v", absErr)
		}
		script := "import json, sys\n" +
			"sys.path.insert(0, " + strconv.Quote(sdkDir) + ")\n" +
			"from rame_sdk import BlindEncoder, SubUnit\n" +
			"enc = BlindEncoder(" + secretLit + ".encode(\"utf-8\"))\n" +
			"subs = [\n"
		for _, su := range subUnits {
			script += "  SubUnit(kind=" + strconv.Quote(su.Kind) +
				", text=" + strconv.Quote(su.Text) +
				", id=" + strconv.Quote(su.ID) +
				", block_id=" + strconv.Quote(su.BlockID) +
				", file_parse=" + strconv.Quote(su.FileParse) +
				", line_start=" + strconv.Itoa(su.LineStart) +
				", line_end=" + strconv.Itoa(su.LineEnd) + "),\n"
		}
		script += "]\n" +
			"packets, sub_to_parent = enc.pack_composite_packets(" + parentIDLit + ", " + parentTextLit + ", " + strconv.FormatInt(occurredAt, 10) + ", subs)\n" +
			"print(json.dumps({\"packets\": packets, \"sub_to_parent\": sub_to_parent}, ensure_ascii=False, sort_keys=True))\n"
		probePath := filepath.Join(dir, "composite_probe.py")
		if err := os.WriteFile(probePath, []byte(script), 0o600); err != nil {
			t.Fatalf("write python probe: %v", err)
		}
		out, err := exec.Command(python, probePath).CombinedOutput()
		if err != nil {
			t.Fatalf("python3 composite probe failed: %v\n%s", err, string(out))
		}
		compareCompositeProbe(t, "python3", out, want)
		t.Log("✓ python3 composite parity: parent + sub-units + sub_to_parent identical")
	}

	if node, err := exec.LookPath("node"); err == nil {
		sdkFile, absErr := filepath.Abs(filepath.Join("..", "nodejs", "index.mjs"))
		if absErr != nil {
			t.Fatalf("resolve node sdk file: %v", absErr)
		}
		specifier := (&url.URL{Scheme: "file", Path: sdkFile}).String()
		script := "import { BlindEncoder } from " + strconv.Quote(specifier) + ";\n" +
			"const enc = new BlindEncoder(" + secretLit + ");\n" +
			"const subs = [\n"
		for _, su := range subUnits {
			script += "  {kind:" + strconv.Quote(su.Kind) +
				", text:" + strconv.Quote(su.Text) +
				", id:" + strconv.Quote(su.ID) +
				", blockId:" + strconv.Quote(su.BlockID) +
				", fileParse:" + strconv.Quote(su.FileParse) +
				", lineStart:" + strconv.Itoa(su.LineStart) +
				", lineEnd:" + strconv.Itoa(su.LineEnd) + "},\n"
		}
		script += "];\n" +
			"const { packets, subToParent } = enc.packCompositePackets(" + parentIDLit + ", " + parentTextLit + ", " + strconv.FormatInt(occurredAt, 10) + ", subs);\n" +
			"const json = JSON.stringify({packets, sub_to_parent: subToParent}, (_k, v) => (typeof v === \"bigint\" ? \"__BIGINT_\" + v.toString() + \"__\" : v)).replace(/\"__BIGINT_(\\d+)__\"/g, \"$1\");\n" +
			"process.stdout.write(json + \"\\n\");\n"
		probePath := filepath.Join(dir, "composite_probe.mjs")
		if err := os.WriteFile(probePath, []byte(script), 0o600); err != nil {
			t.Fatalf("write node probe: %v", err)
		}
		out, err := exec.Command(node, probePath).CombinedOutput()
		if err != nil {
			t.Fatalf("node composite probe failed: %v\n%s", err, string(out))
		}
		compareCompositeProbe(t, "node", out, want)
		t.Log("✓ node composite parity: parent + sub-units + sub_to_parent identical")
	}

	if java, javac := javaToolchain(); java != "" && javac != "" {
		sdkJava, absErr := filepath.Abs(filepath.Join("..", "java", "RameSdk.java"))
		if absErr != nil {
			t.Fatalf("resolve java sdk file: %v", absErr)
		}
		src, readErr := os.ReadFile(sdkJava)
		if readErr != nil {
			t.Fatalf("read java sdk: %v", readErr)
		}
		javaDir := filepath.Join(dir, "java")
		if err := os.MkdirAll(javaDir, 0o700); err != nil {
			t.Fatalf("mkdir java probe dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(javaDir, "RameSdk.java"), src, 0o600); err != nil {
			t.Fatalf("copy java sdk: %v", err)
		}
		probe := "import java.nio.charset.StandardCharsets;\n" +
			"import java.util.ArrayList;\n" +
			"import java.util.List;\n" +
			"import java.util.Map;\n" +
			"public final class CompositeProbe {\n" +
			"  static String q(String s) {\n" +
			"    StringBuilder sb = new StringBuilder(\"\\\"\");\n" +
			"    for (int i = 0; i < s.length(); i++) {\n" +
			"      char c = s.charAt(i);\n" +
			"      if (c == '\"' || c == '\\\\') sb.append('\\\\');\n" +
			"      sb.append(c);\n" +
			"    }\n" +
			"    return sb.append('\"').toString();\n" +
			"  }\n" +
			"  public static void main(String[] args) {\n" +
			"    RameSdk.BlindEncoder enc = new RameSdk.BlindEncoder(" + javaString(secret) + ".getBytes(StandardCharsets.UTF_8));\n" +
			"    List<RameSdk.SubUnit> subs = new ArrayList<>();\n"
		for _, su := range subUnits {
			probe += "    subs.add(new RameSdk.SubUnit(" + javaString(su.Kind) + ", " + javaString(su.Text) + ", " +
				javaString(su.ID) + ", " + javaString(su.BlockID) + ", " + javaString(su.FileParse) + ", " +
				strconv.Itoa(su.LineStart) + ", " + strconv.Itoa(su.LineEnd) + "));\n"
		}
		probe += "    RameSdk.CompositePackets out = enc.packCompositePackets(" + javaString(parentID) + ", " + javaString(parentText) + ", " + strconv.FormatInt(occurredAt, 10) + "L, subs);\n" +
			"    StringBuilder sb = new StringBuilder(\"{\\\"packets\\\":[\");\n" +
			"    for (int i = 0; i < out.packets.size(); i++) { if (i > 0) sb.append(','); sb.append(out.packets.get(i).toJson()); }\n" +
			"    sb.append(\"],\\\"sub_to_parent\\\":{\");\n" +
			"    boolean first = true;\n" +
			"    for (Map.Entry<String, String> e : out.subToParent.entrySet()) { if (!first) sb.append(','); first = false; sb.append(q(e.getKey())).append(':').append(q(e.getValue())); }\n" +
			"    sb.append(\"}}\");\n" +
			"    System.out.println(sb);\n" +
			"  }\n" +
			"}\n"
		probePath := filepath.Join(javaDir, "CompositeProbe.java")
		if err := os.WriteFile(probePath, []byte(probe), 0o600); err != nil {
			t.Fatalf("write java probe: %v", err)
		}
		classes := filepath.Join(javaDir, "classes")
		if err := os.MkdirAll(classes, 0o700); err != nil {
			t.Fatalf("mkdir java classes: %v", err)
		}
		compile := exec.Command(javac, "-d", classes, filepath.Join(javaDir, "RameSdk.java"), probePath)
		if out, err := compile.CombinedOutput(); err != nil {
			t.Fatalf("javac composite probe failed: %v\n%s", err, string(out))
		}
		run := exec.Command(java, "-cp", classes, "CompositeProbe")
		out, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("java composite probe failed: %v\n%s", err, string(out))
		}
		compareCompositeProbe(t, "java", out, want)
		t.Log("✓ java composite parity: parent + sub-units + sub_to_parent identical")
	}
}

// TestCrossLanguageUnitMetaPacketParity verifies the `UnitMeta` surface —
// including `file_parse`, `file_url`/`doc_h`, `block_id`/`block_h`, the
// tri-temporal timestamps and the author commitment — is a 1:1 runtime
// capability across all four SDKs.
func TestCrossLanguageUnitMetaPacketParity(t *testing.T) {
	secret := "rame-unitmeta-parity-key-v1"
	docID := "sgm-meta-1"
	text := "ID: sgm-meta-1\nSummary: groceries"
	meta := ramesdk.UnitMeta{
		Author:     "Alice",
		OccurredAt: 1700000000,
		NarratedAt: 1700000500,
		FileURL:    "mail:2022-01-01",
		FileParse:  "Subject: delivery",
		BlockID:    "sgm-meta-1#header",
		LineStart:  3,
		LineEnd:    5,
	}
	enc := ramesdk.NewBlindEncoder([]byte(secret))
	packet, _ := enc.PackBlindPacketWithMeta(docID, text, meta)
	want := compositeProbeResult{Packets: []ramesdk.BlindPulsePacket{packet}, SubToParent: map[string]string{}}
	if packet.DocHash == 0 || packet.BlockHash == 0 || packet.AuthorHash == 0 || packet.LineStart != 3 || packet.LineEnd != 5 {
		t.Fatalf("Go UnitMeta fixture lost commitments: %+v", packet)
	}

	dir := t.TempDir()

	if python, err := exec.LookPath("python3"); err == nil {
		sdkDir, absErr := filepath.Abs(filepath.Join("..", "python"))
		if absErr != nil {
			t.Fatalf("resolve python sdk dir: %v", absErr)
		}
		script := "import json, sys\n" +
			"sys.path.insert(0, " + strconv.Quote(sdkDir) + ")\n" +
			"from rame_sdk import BlindEncoder\n" +
			"enc = BlindEncoder(" + strconv.Quote(secret) + ".encode(\"utf-8\"))\n" +
			"pkt, _ = enc.pack_blind_packet_with_meta(" + strconv.Quote(docID) + ", " + strconv.Quote(text) +
			", author=" + strconv.Quote(meta.Author) +
			", occurred_at=" + strconv.FormatInt(meta.OccurredAt, 10) +
			", narrated_at=" + strconv.FormatInt(meta.NarratedAt, 10) +
			", file_url=" + strconv.Quote(meta.FileURL) +
			", file_parse=" + strconv.Quote(meta.FileParse) +
			", block_id=" + strconv.Quote(meta.BlockID) +
			", line_start=" + strconv.Itoa(meta.LineStart) +
			", line_end=" + strconv.Itoa(meta.LineEnd) + ")\n" +
			"print(json.dumps({\"packets\": [pkt], \"sub_to_parent\": {}}, ensure_ascii=False, sort_keys=True))\n"
		probePath := filepath.Join(dir, "meta_probe.py")
		if err := os.WriteFile(probePath, []byte(script), 0o600); err != nil {
			t.Fatalf("write python meta probe: %v", err)
		}
		out, err := exec.Command(python, probePath).CombinedOutput()
		if err != nil {
			t.Fatalf("python3 meta probe failed: %v\n%s", err, string(out))
		}
		compareCompositeProbe(t, "python3", out, want)
		t.Log("✓ python3 UnitMeta parity: file_parse/spatial/tri-temporal commitments identical")
	}

	if node, err := exec.LookPath("node"); err == nil {
		sdkFile, absErr := filepath.Abs(filepath.Join("..", "nodejs", "index.mjs"))
		if absErr != nil {
			t.Fatalf("resolve node sdk file: %v", absErr)
		}
		specifier := (&url.URL{Scheme: "file", Path: sdkFile}).String()
		script := "import { BlindEncoder } from " + strconv.Quote(specifier) + ";\n" +
			"const enc = new BlindEncoder(" + strconv.Quote(secret) + ");\n" +
			"const { packet } = enc.packBlindPacketWithMeta(" + strconv.Quote(docID) + ", " + strconv.Quote(text) +
			", {author:" + strconv.Quote(meta.Author) +
			", occurredAt:" + strconv.FormatInt(meta.OccurredAt, 10) +
			", narratedAt:" + strconv.FormatInt(meta.NarratedAt, 10) +
			", fileUrl:" + strconv.Quote(meta.FileURL) +
			", fileParse:" + strconv.Quote(meta.FileParse) +
			", blockId:" + strconv.Quote(meta.BlockID) +
			", lineStart:" + strconv.Itoa(meta.LineStart) +
			", lineEnd:" + strconv.Itoa(meta.LineEnd) + "});\n" +
			"const json = JSON.stringify({packets: [packet], sub_to_parent: {}}, (_k, v) => (typeof v === \"bigint\" ? \"__BIGINT_\" + v.toString() + \"__\" : v)).replace(/\"__BIGINT_(\\d+)__\"/g, \"$1\");\n" +
			"process.stdout.write(json + \"\\n\");\n"
		probePath := filepath.Join(dir, "meta_probe.mjs")
		if err := os.WriteFile(probePath, []byte(script), 0o600); err != nil {
			t.Fatalf("write node meta probe: %v", err)
		}
		out, err := exec.Command(node, probePath).CombinedOutput()
		if err != nil {
			t.Fatalf("node meta probe failed: %v\n%s", err, string(out))
		}
		compareCompositeProbe(t, "node", out, want)
		t.Log("✓ node UnitMeta parity: file_parse/spatial/tri-temporal commitments identical")
	}

	if java, javac := javaToolchain(); java != "" && javac != "" {
		sdkJava, absErr := filepath.Abs(filepath.Join("..", "java", "RameSdk.java"))
		if absErr != nil {
			t.Fatalf("resolve java sdk file: %v", absErr)
		}
		src, readErr := os.ReadFile(sdkJava)
		if readErr != nil {
			t.Fatalf("read java sdk: %v", readErr)
		}
		javaDir := filepath.Join(dir, "java")
		if err := os.MkdirAll(javaDir, 0o700); err != nil {
			t.Fatalf("mkdir java meta probe dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(javaDir, "RameSdk.java"), src, 0o600); err != nil {
			t.Fatalf("copy java sdk: %v", err)
		}
		probe := "import java.nio.charset.StandardCharsets;\n" +
			"public final class MetaProbe {\n" +
			"  public static void main(String[] args) {\n" +
			"    RameSdk.BlindEncoder enc = new RameSdk.BlindEncoder(" + javaString(secret) + ".getBytes(StandardCharsets.UTF_8));\n" +
			"    RameSdk.UnitMeta meta = new RameSdk.UnitMeta(" + javaString(meta.Author) + ", " + strconv.FormatInt(meta.OccurredAt, 10) + "L, " +
			strconv.FormatInt(meta.NarratedAt, 10) + "L, 0L, " + javaString(meta.FileURL) + ", " + javaString(meta.FileParse) + ", " +
			javaString(meta.BlockID) + ", " + strconv.Itoa(meta.LineStart) + ", " + strconv.Itoa(meta.LineEnd) + ");\n" +
			"    RameSdk.BlindPulsePacket packet = enc.packBlindPacketWithMeta(" + javaString(docID) + ", " + javaString(text) + ", meta);\n" +
			"    System.out.println(\"{\\\"packets\\\":[\" + packet.toJson() + \"],\\\"sub_to_parent\\\":{}}\");\n" +
			"  }\n" +
			"}\n"
		probePath := filepath.Join(javaDir, "MetaProbe.java")
		if err := os.WriteFile(probePath, []byte(probe), 0o600); err != nil {
			t.Fatalf("write java meta probe: %v", err)
		}
		classes := filepath.Join(javaDir, "classes")
		if err := os.MkdirAll(classes, 0o700); err != nil {
			t.Fatalf("mkdir java meta classes: %v", err)
		}
		compile := exec.Command(javac, "-d", classes, filepath.Join(javaDir, "RameSdk.java"), probePath)
		if out, err := compile.CombinedOutput(); err != nil {
			t.Fatalf("javac meta probe failed: %v\n%s", err, string(out))
		}
		run := exec.Command(java, "-cp", classes, "MetaProbe")
		out, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("java meta probe failed: %v\n%s", err, string(out))
		}
		compareCompositeProbe(t, "java", out, want)
		t.Log("✓ java UnitMeta parity: file_parse/spatial/tri-temporal commitments identical")
	}
}

func compareCompositeProbe(t *testing.T, lang string, raw []byte, want compositeProbeResult) {
	t.Helper()
	var got compositeProbeResult
	if err := json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatalf("%s composite probe output is not valid JSON: %v\nraw: %s", lang, err, string(raw))
	}
	if !reflect.DeepEqual(got, want) {
		wantRaw, _ := json.MarshalIndent(want, "", "  ")
		gotRaw, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("%s composite packet parity mismatch\nwant: %s\ngot:  %s", lang, wantRaw, gotRaw)
	}
}

// javaToolchain returns a runnable java/javac pair, preferring explicit
// RAME_JAVA_HOME/JAVA_HOME over a possibly-stubbed `java` on PATH. It skips
// silently (returns empty strings) when no working toolchain exists.
func javaToolchain() (string, string) {
	for _, home := range []string{os.Getenv("RAME_JAVA_HOME"), os.Getenv("JAVA_HOME")} {
		if home == "" {
			continue
		}
		java := filepath.Join(home, "bin", "java")
		javac := filepath.Join(home, "bin", "javac")
		if fileExists(java) && fileExists(javac) {
			return java, javac
		}
	}
	java, err := exec.LookPath("java")
	if err != nil {
		return "", ""
	}
	if out, err := exec.Command(java, "-version").CombinedOutput(); err != nil || len(out) == 0 {
		return "", ""
	}
	javac, err := exec.LookPath("javac")
	if err != nil {
		return "", ""
	}
	return java, javac
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func javaString(value string) string {
	return strconv.Quote(value)
}
