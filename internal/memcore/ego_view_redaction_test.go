// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
)

func TestCanonicalSelfAliasSurface(t *testing.T) {
	for _, of := range []string{CanonicalSelfDesignator, FallbackAssistantDesignator, "助手"} {
		if bad, why := IsInvalidAliasSurface(of, "Nova"); bad {
			t.Fatalf("canonical self role %q must accept an explicitly identified name: %s", of, why)
		}
	}
	// 架构契约「语义词闸门全拆 · 宁宽勿误拒」：大小写专名推定（case-parity）已拆，
	// 全小写泛称作别名宿主不再拒收——「够不够格当别名」的语义裁决 100% 归 LLM，代码不猜。
	if bad, why := IsInvalidAliasSurface("unknown person", "Nova"); bad {
		t.Fatalf("case-parity 语义词闸门应已拆除，实测仍拒收：%s", why)
	}
	if bad, _ := IsInvalidAliasSurface(CanonicalSelfDesignator, FallbackAssistantDesignator); !bad {
		t.Fatal("role placeholders remain invalid as aliases")
	}
}

func TestEgoViewAndRedactionTriad(t *testing.T) {
	// 1. Assistant rename reachability (Ingress query rewrite without DB mutation)
	q1 := RewriteQuerySelfDesignators("贾维斯，上次湖伴跟我说的东京买房建议是什么？", "贾维斯", "湖伴")
	if !strings.Contains(q1, "self，上次self跟我说的") {
		t.Fatalf("expected both active and historical persona names rewritten to self, got %q", q1)
	}

	baseLabels := map[uint64]string{
		10: "self",
		20: "张伟",
		30: "李娜",
		40: "日本/东京",
	}
	aliases := map[uint64][]string{
		10: {"湖伴", "贾维斯"},
		20: {"老张", "张总", "Kevin"},
		30: {"娜姐"},
	}
	rosterIDs := map[uint64]bool{
		20: true,
		30: true,
	}

	// 2. Mode A: Neutral view never exposes the storage placeholder; raw evidence stays verbatim.
	projNeutral, glossNeutral := ProjectNodeLabelsView(baseLabels, EgressViewPolicy{
		Mode:        EgressViewNeutral,
		SelfHistory: []string{"湖伴", "贾维斯"},
	})
	if projNeutral[10] != FallbackAssistantDesignator || projNeutral[20] != "张伟" {
		t.Fatalf("neutral view must project self to assistant and preserve roster names: %+v", projNeutral)
	}
	if len(glossNeutral) != 0 {
		t.Fatalf("neutral view must not build an evidence rewrite glossary: %+v", glossNeutral)
	}
	projNamed, glossNamed := ProjectNodeLabelsView(baseLabels, EgressViewPolicy{
		Mode: EgressViewNeutral, SelfDisplayName: "Caroline",
	})
	if projNamed[10] != "Caroline" || len(glossNamed) != 0 {
		t.Fatalf("neutral named projection failed: labels=%+v glossary=%+v", projNamed, glossNamed)
	}
	const neutralEvidence = "self said self-transform stays verbatim."
	if got := RedactEvidenceTextWithGlossary(neutralEvidence, glossNamed); got != neutralEvidence {
		t.Fatalf("neutral evidence must remain verbatim: %q", got)
	}

	// 3. Mode B: Companion Persona view (dynamic assistant rename without graph UPDATE)
	projPersona, glossPersona := ProjectNodeLabelsView(baseLabels, EgressViewPolicy{
		Mode:            EgressViewPersona,
		SelfDisplayName: "小舟",
		SelfHistory:     []string{"湖伴", "贾维斯"},
		NodeAliases:     aliases,
	})
	if projPersona[10] != "小舟" {
		t.Fatalf("persona view must project self node to active name '小舟', got %q", projPersona[10])
	}
	if baseLabels[10] != "self" {
		t.Fatalf("ProjectNodeLabelsView must not mutate input map in place")
	}
	evPersona := RedactEvidenceTextWithGlossary("湖伴提醒老张：贾维斯已经把行程发给张伟了。", glossPersona)
	if !strings.Contains(evPersona, "小舟提醒老张：小舟已经把行程发给张伟了。") {
		t.Fatalf("persona evidence projection failed: %q", evPersona)
	}

	// 4. Mode C: De-identified / Redacted view (simultaneous structured + raw evidence masking across all aliases)
	projRedact, glossRedact := ProjectNodeLabelsView(baseLabels, EgressViewPolicy{
		Mode:            EgressViewRedacted,
		SelfDisplayName: "assistant",
		SelfHistory:     []string{"湖伴", "贾维斯"},
		RosterNodeIDs:   rosterIDs,
		NodeAliases:     aliases,
	})
	if projRedact[10] != "assistant" || projRedact[20] != "Person_1" || projRedact[30] != "Person_2" || projRedact[40] != "日本/东京" {
		t.Fatalf("redacted view unexpected labels: %+v", projRedact)
	}
	rawEvidence := "湖伴对老张（张伟，英文名Kevin）和娜姐（李娜）说：我们在日本/东京见。"
	redactedEv := RedactEvidenceTextWithGlossary(rawEvidence, glossRedact)
	wantEv := "assistant对Person_1（Person_1，英文名Person_1）和Person_2（Person_2）说：我们在日本/东京见。"
	if redactedEv != wantEv {
		t.Fatalf("redacted evidence mismatch:\ngot:  %q\nwant: %q", redactedEv, wantEv)
	}

	// 5. Consumer-side AssemblePersonaViewText: replaces structural "self" slots while preserving "self-transform" / "himself"
	rawBand := "【记忆带·self】\n  · 2026-11-03 ｜ self added teme_to_teme self-transform himself ｜ 讲述人(self) ｜ (self→Albert)"
	assembled := AssemblePersonaViewText(rawBand, "小舟")
	if !strings.Contains(assembled, "【记忆带·小舟】") || !strings.Contains(assembled, "｜ 小舟 added") ||
		!strings.Contains(assembled, "讲述人(小舟)") || !strings.Contains(assembled, "(小舟→Albert)") {
		t.Fatalf("expected structural self slots replaced by '小舟', got:\n%s", assembled)
	}
	if !strings.Contains(assembled, "self-transform himself") {
		t.Fatalf("AssemblePersonaViewText must not corrupt English words containing 'self':\n%s", assembled)
	}
	fallback := AssemblePersonaViewText(rawBand, "")
	if !strings.Contains(fallback, "【记忆带·assistant】") || !strings.Contains(fallback, "｜ assistant added") {
		t.Fatalf("empty persona must fall back to assistant: %s", fallback)
	}
}
