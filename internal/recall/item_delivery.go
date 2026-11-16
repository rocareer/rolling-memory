// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/store"
)

type flatCandidateKind int

const (
	flatKindAssertion flatCandidateKind = iota
	flatKindUnit
	flatKindParse
	flatKindBlock
)

type flatDeliveryCandidate struct {
	kind     flatCandidateKind
	occDate  string
	spkDate  string
	text     string
	locator  string
	relScore float64
	seq      int
	// srcHead is the leading segment of the candidate's source text (its
	// self-identifying head line, bounded), keeping the source identity inside
	// every entry capsule so delivered entries remain resolvable back to source items.
	srcHead string
}

type pureAssertionItem struct {
	occDate string
	spkDate string
	fact    string
	turnKey string
	score   float64
	seq     int
	srcHead string
}

// ReconcileClauseEventDate calibrates a clause's event date (occDate) against its own relative time
// expression relative to the spoken date (spkDate) when the turn already carries a cross-day shift
// (occDate != spkDate), preventing cross-clause date misattribution.
func ReconcileClauseEventDate(clauseText, occDate, spkDate string) string {
	spkTrim := strings.TrimSpace(spkDate)
	occTrim := strings.TrimSpace(occDate)
	if len(spkTrim) < 10 || occTrim == spkTrim {
		return occTrim
	}
	tSpk, err := time.Parse("2006-01-02", spkTrim[:10])
	if err != nil {
		return occTrim
	}
	if tag := strings.TrimSpace(memcore.ResolveRelativeTag(clauseText, tSpk.Unix())); tag != "" {
		if openIdx := strings.Index(tag, "（"); openIdx > 0 {
			tag = strings.TrimSpace(tag[:openIdx])
		}
		if tag != "" {
			return tag
		}
	}
	return occTrim
}

// isInvalidOrPartDerivedAssertion filters out 0-information self-contained phrase decomposition tautologies
// where all content tokens of the object already appear inside the subject.
func isInvalidOrPartDerivedAssertion(fact string) bool {
	low := strings.ToLower(strings.TrimSpace(fact))
	if low == "" {
		return true
	}
	for _, sep := range []string{" is a ", " is an ", " includes ", " has ", " depicts ", " —is a→ ", " —is an→ ", " —includes→ ", " —has→ ", " —depicts→ ", " — is a → ", " — is an → ", " — includes → ", " — has → ", " — depicts → "} {
		if idx := strings.Index(low, sep); idx > 0 {
			lhs := strings.TrimSpace(low[:idx])
			rhs := strings.TrimSpace(low[idx+len(sep):])
			if lhs != "" && rhs != "" {
				if strings.Contains(lhs, rhs) || strings.Contains(rhs, lhs) {
					return true
				}
				lSet := make(map[string]bool, 8)
				for _, lt := range memcore.Tokenize(lhs) {
					lSet[strings.ToLower(lt)] = true
				}
				rToks := memcore.Tokenize(rhs)
				if len(rToks) > 0 {
					allInLHS := true
					for _, rt := range rToks {
						if !lSet[strings.ToLower(rt)] {
							allInLHS = false
							break
						}
					}
					if allInLHS {
						return true
					}
				}
			}
		}
	}
	return false
}

// foldSubordinatePureAssertionItems folds subordinate structured memory assertion entries whose fact string or token set is
// strictly subsumed by another entry on the same spoken/occurrence date.
func foldSubordinatePureAssertionItems(entries []pureAssertionItem) []pureAssertionItem {
	if len(entries) <= 1 {
		return entries
	}
	norms := make([]string, len(entries))
	tokSets := make([]map[string]bool, len(entries))
	for i := range entries {
		norms[i] = strings.ToLower(strings.TrimSpace(entries[i].fact))
		ts := make(map[string]bool, 8)
		for _, t := range memcore.Tokenize(norms[i]) {
			if tl := strings.ToLower(strings.TrimSpace(t)); tl != "" {
				ts[tl] = true
			}
		}
		tokSets[i] = ts
	}
	out := make([]pureAssertionItem, 0, len(entries))
	for i, ei := range entries {
		ni := norms[i]
		if ni == "" {
			continue
		}
		subsumed := false
		for j, ej := range entries {
			if i == j {
				continue
			}
			nj := norms[j]
			if nj == "" || ni == nj {
				continue
			}
			if ei.spkDate != ej.spkDate && ei.occDate != ej.occDate {
				continue
			}
			if strings.Contains(nj, ni) {
				subsumed = true
				break
			}
			if len(tokSets[i]) > 0 && len(tokSets[j]) >= len(tokSets[i]) {
				allInJ := true
				for tk := range tokSets[i] {
					if !tokSets[j][tk] {
						allInJ = false
						break
					}
				}
				if allInJ && (len(tokSets[j]) > len(tokSets[i]) || j < i) {
					subsumed = true
					break
				}
			}
		}
		if !subsumed {
			out = append(out, ei)
		}
	}
	return out
}

func hasLeadingSpeakerPrefix(s string) bool {
	trimmed := strings.TrimSpace(s)
	cIdx := strings.Index(trimmed, ": ")
	if cIdx <= 0 || strings.ContainsAny(trimmed[:cIdx], ".,!?/\\") {
		return false
	}
	return memcore.CountTokens(trimmed[:cIdx]) <= memcore.TokenTier4
}

func splitCandidateClauses(raw string) []string {
	cleaned := strings.ReplaceAll(raw, " → ", "\n")
	var out []string
	for _, line := range strings.Split(cleaned, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "【原文") || strings.HasPrefix(trimmed, "【证据") {
			continue
		}
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") || strings.ContainsAny(trimmed, "{}();=") {
			out = append(out, trimmed)
			continue
		}
		speakerPrefix := ""
		body := trimmed
		if cIdx := strings.Index(trimmed, ": "); cIdx > 0 && !strings.ContainsAny(trimmed[:cIdx], ".,!?/\\") {
			candSpk := strings.TrimSpace(trimmed[:cIdx])
			if memcore.CountTokens(candSpk) <= memcore.TokenTier4 {
				speakerPrefix = candSpk + ": "
				body = strings.TrimSpace(trimmed[cIdx+2:])
			}
		}
		var segBuf strings.Builder
		firstEmitted := false
		emitSeg := func() {
			seg := strings.TrimSpace(segBuf.String())
			segBuf.Reset()
			if seg == "" {
				return
			}
			if !firstEmitted && speakerPrefix != "" && !hasLeadingSpeakerPrefix(seg) {
				out = append(out, speakerPrefix+seg)
			} else {
				out = append(out, seg)
			}
			firstEmitted = true
		}
		for _, r := range body {
			segBuf.WriteRune(r)
			if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' {
				emitSeg()
			}
		}
		emitSeg()
	}
	return out
}

// ExtractTurnUncoveredResidualClauses computes Text(t) \ LocalAssertion(t), stripping clauses already
// covered by local structured memory assertion facts while preserving uncovered details, relative time expressions, and numbers.
func ExtractTurnUncoveredResidualClauses(
	rawText string,
	localAssertionFacts []string,
	deliveredResiduals map[string]bool,
	allTokens []string,
) []string {
	txt := memcore.CompactSupplementImageElaboration(strings.TrimSpace(rawText), allTokens)
	if txt == "" {
		return nil
	}
	clauses := splitCandidateClauses(txt)
	var kept []string
	isCoveredByLocalAssertion := func(seg string) bool {
		sClean := strings.TrimSpace(strings.Trim(seg, ".!?。！？\"'「」 "))
		if sClean == "" {
			return true
		}
		hasRelTime := memcore.ResolveRelativeTag(sClean, 1700000000) != ""
		sLow := strings.ToLower(sClean)
		for _, f := range localAssertionFacts {
			fLow := strings.ToLower(f)
			if strings.Contains(fLow, sLow) {
				return true
			}
			if !hasRelTime && memcore.StmtCoversSnippet(f, sClean) {
				return true
			}
		}
		return false
	}

	for _, cl := range clauses {
		clTrim := strings.TrimSpace(cl)
		if clTrim == "" {
			continue
		}
		coreBody := clTrim
		spkHead := ""
		if cIdx := strings.Index(clTrim, ": "); cIdx > 0 && memcore.CountTokens(clTrim[:cIdx]) <= memcore.TokenTier4 {
			spkHead = clTrim[:cIdx+2]
			coreBody = strings.TrimSpace(clTrim[cIdx+2:])
		}
		subParts := strings.FieldsFunc(coreBody, func(r rune) bool {
			return r == ',' || r == ';' || r == '，' || r == '；'
		})
		if len(subParts) > 1 {
			var keptSubs []string
			for _, sp := range subParts {
				if !isCoveredByLocalAssertion(sp) {
					keptSubs = append(keptSubs, strings.TrimSpace(sp))
				}
			}
			if len(keptSubs) == 0 {
				continue
			}
			if len(keptSubs) < len(subParts) {
				coreBody = strings.Join(keptSubs, ", ")
				clTrim = spkHead + coreBody
				highGainCnt := 0
				for _, tk := range memcore.Tokenize(coreBody) {
					if memcore.IsHighGainToken(strings.ToLower(tk)) {
						highGainCnt++
					}
				}
				if highGainCnt <= 1 && !strings.ContainsAny(coreBody, "\"'0123456789") {
					continue
				}
			}
		}
		if isCoveredByLocalAssertion(coreBody) {
			continue
		}
		if memcore.StringShannonBits(coreBody) < 6.0 {
			continue
		}
		normKey := strings.ToLower(coreBody)
		if deliveredResiduals[normKey] {
			continue
		}
		deliveredResiduals[normKey] = true
		kept = append(kept, clTrim)
	}
	return kept
}

func matchMorphTokenInText(textLow, ql string) float64 {
	if textLow == "" || ql == "" {
		return 0
	}
	if strings.Contains(textLow, ql) {
		return 1.0
	}
	if m := memcore.MatchTokenOrStem(textLow, ql); m >= 0.55 {
		return m
	}
	if st := memcore.StemToken(ql); st != "" && st != ql && memcore.StringShannonBits(st) >= memcore.AtomicConceptEntropyBits {
		if strings.Contains(textLow, st) {
			return 0.85
		}
	}
	return 0
}

// extractInlineQuotesFromLine extracts inline verbatim quotes and follow-up quotes
// ("原文:「...」" / "Verbatim: \"...\"" / "追问续答:「...」" / "Follow-up: \"...\"") from a rendered line.
func extractInlineQuotesFromLine(line string) string {
	var inlineParts []string
	for _, part := range strings.Split(line, "｜") {
		pTrim := strings.TrimSpace(part)
		for _, qPrefix := range []string{"原文:「", "原文:\"", "Verbatim: \"", "追问续答:「", "追问续答:\"", "Follow-up: \""} {
			if qIdx := strings.Index(pTrim, qPrefix); qIdx >= 0 {
				qRem := pTrim[qIdx+len(qPrefix):]
				qRem = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(qRem), "」"), "\"")
				if qRem != "" {
					inlineParts = append(inlineParts, qRem)
				}
			}
		}
	}
	return strings.Join(inlineParts, "\n")
}

func computeUnifiedCandidateRelevance(baseRankScore float64, text string, allTokens []string) float64 {
	sc := baseRankScore
	if len(allTokens) > 0 {
		ov := memcore.ShannonTokenOverlapSim(text, allTokens)
		textLow := strings.ToLower(text)
		hitTerms := 0
		morphSimSum := 0.0
		seenQ := make(map[string]bool, len(allTokens))
		for _, qt := range allTokens {
			ql := strings.ToLower(strings.TrimSpace(qt))
			if ql == "" || seenQ[ql] || !memcore.IsHighGainToken(ql) {
				continue
			}
			seenQ[ql] = true
			if m := matchMorphTokenInText(textLow, ql); m > 0 {
				hitTerms++
				morphSimSum += m
			}
		}
		if len(seenQ) > 0 {
			if morphOv := morphSimSum / float64(len(seenQ)); morphOv > ov {
				ov = morphOv
			}
		}
		sc = 0.65*baseRankScore + 0.65*ov
		if hitTerms >= 2 {
			sc += 0.22 * float64(hitTerms-1)
		}
	}
	bits := memcore.StringShannonBits(text)
	if bits > 0 {
		sc += 0.02 * math.Min(1.0, bits/24.0)
	}
	return sc
}

// prioritizeEvidenceInTopKPool ensures that when a top_k (capN) budget is specified,
// query-matching residual evidence lines (flatKindUnit / flatKindParse) and evidence blocks
// (flatKindBlock) receive guaranteed seats while reserving at least max(4, capN*3/5) seats
// for core structured memory assertions.
func prioritizeEvidenceInTopKPool(
	fullSortedPool []flatDeliveryCandidate,
	selectedPool []flatDeliveryCandidate,
	allTokens []string,
	capN int,
) []flatDeliveryCandidate {
	if capN < 2 || len(fullSortedPool) <= len(selectedPool) {
		return selectedPool
	}
	inPoolSeq := make(map[int]bool, len(selectedPool))
	curEvLines, curEvArts := 0, 0
	for _, c := range selectedPool {
		inPoolSeq[c.seq] = true
		if c.kind == flatKindUnit || c.kind == flatKindParse {
			curEvLines++
		} else if c.kind == flatKindBlock {
			curEvArts++
		}
	}
	var availEvLines, availEvArts []flatDeliveryCandidate
	for _, c := range fullSortedPool {
		if inPoolSeq[c.seq] {
			continue
		}
		if len(allTokens) > 0 && memcore.ShannonTokenOverlapSim(c.text, allTokens) <= 0 {
			continue
		}
		if c.kind == flatKindUnit || c.kind == flatKindParse {
			availEvLines = append(availEvLines, c)
		} else if c.kind == flatKindBlock {
			availEvArts = append(availEvArts, c)
		}
	}
	if len(availEvLines) == 0 && len(availEvArts) == 0 {
		return selectedPool
	}
	assertionTotal := 0
	for _, c := range selectedPool {
		if c.kind == flatKindAssertion {
			assertionTotal++
		}
	}
	minAssertionReserve := 1
	if capN >= 20 {
		minAssertionReserve = capN * 3 / 5
	} else if capN >= 6 {
		minAssertionReserve = max(4, capN/2)
	}
	if minAssertionReserve > assertionTotal {
		minAssertionReserve = assertionTotal
	}
	maxEvTotal := capN - minAssertionReserve
	if maxEvTotal < 1 {
		maxEvTotal = 1
	}
	curEvTotal := curEvLines + curEvArts
	if curEvTotal >= maxEvTotal {
		if curEvArts == 0 && len(availEvArts) > 0 && assertionTotal > 1 {
			maxEvTotal = curEvTotal + 1
		} else if curEvLines == 0 && len(availEvLines) > 0 && assertionTotal > 1 {
			maxEvTotal = curEvTotal + 1
		} else {
			return selectedPool
		}
	}
	budgetToInject := maxEvTotal - curEvTotal
	var mustInject []flatDeliveryCandidate
	artIdx, lineIdx := 0, 0
	if curEvArts == 0 && len(availEvArts) > 0 && len(mustInject) < budgetToInject {
		mustInject = append(mustInject, availEvArts[0])
		artIdx = 1
	}
	if curEvLines == 0 && len(availEvLines) > 0 && len(mustInject) < budgetToInject {
		mustInject = append(mustInject, availEvLines[0])
		lineIdx = 1
	}
	assertionScores := make([]float64, 0, assertionTotal)
	for _, c := range selectedPool {
		if c.kind == flatKindAssertion {
			assertionScores = append(assertionScores, c.relScore)
		}
	}
	freeRoom := max(0, capN-len(selectedPool))
	for len(mustInject) < budgetToInject && (artIdx < len(availEvArts) || lineIdx < len(availEvLines)) {
		takeArt := false
		if artIdx < len(availEvArts) && lineIdx < len(availEvLines) {
			takeArt = availEvArts[artIdx].relScore >= availEvLines[lineIdx].relScore
		} else if artIdx < len(availEvArts) {
			takeArt = true
		}
		var cand flatDeliveryCandidate
		if takeArt {
			cand = availEvArts[artIdx]
		} else {
			cand = availEvLines[lineIdx]
		}
		willDisplaceAssertion := len(mustInject) >= freeRoom
		if willDisplaceAssertion && !takeArt && capN >= 20 {
			displaceIdx := len(assertionScores) - 1 - (len(mustInject) - freeRoom)
			if displaceIdx < minAssertionReserve || (displaceIdx >= 0 && displaceIdx < len(assertionScores) && cand.relScore < assertionScores[displaceIdx]) {
				break
			}
		}
		if takeArt {
			artIdx++
		} else {
			lineIdx++
		}
		mustInject = append(mustInject, cand)
	}
	if len(mustInject) == 0 {
		return selectedPool
	}
	needRoom := len(selectedPool) + len(mustInject) - capN
	if needRoom > 0 {
		kept := make([]flatDeliveryCandidate, 0, capN)
		dropAssertion := min(needRoom, max(0, assertionTotal-minAssertionReserve))
		if dropAssertion == 0 && curEvArts == 0 && len(availEvArts) > 0 && assertionTotal > 1 {
			dropAssertion = 1
		}
		assertionSeen := 0
		for _, c := range selectedPool {
			if c.kind == flatKindAssertion {
				assertionSeen++
				if assertionSeen > assertionTotal-dropAssertion {
					continue
				}
			}
			kept = append(kept, c)
		}
		selectedPool = kept
	}
	selectedPool = append(selectedPool, mustInject...)
	if len(selectedPool) > capN {
		selectedPool = selectedPool[:capN]
	}
	return selectedPool
}

// reweightPoolByFocalMarginalGain promotes candidates that match scarce focal query tokens
// (pool DF < 0.45) or share a spoken date with a focal time-shifted event.
func reweightPoolByFocalMarginalGain(pool []flatDeliveryCandidate, allTokens []string) {
	if len(pool) <= memcore.TokenTier16 || len(allTokens) == 0 {
		return
	}
	nPool := len(pool)
	poolLow := make([]string, nPool)
	for i := range pool {
		poolLow[i] = strings.ToLower(pool[i].text)
	}
	var focalTokens []string
	for _, qt := range allTokens {
		ql := strings.ToLower(strings.TrimSpace(qt))
		if ql == "" || !memcore.IsHighGainToken(ql) {
			continue
		}
		df := 0
		for i := range pool {
			if matchMorphTokenInText(poolLow[i], ql) > 0 {
				df++
			}
		}
		if float64(df)/float64(nPool) < 0.45 {
			focalTokens = append(focalTokens, ql)
		}
	}
	if len(focalTokens) == 0 {
		focalTokens = allTokens
	}

	focalDates := make(map[string]bool, 8)
	focalOvList := make([]float64, nPool)
	for i := range pool {
		fov := memcore.ShannonTokenOverlapSim(pool[i].text, focalTokens)
		if len(focalTokens) > 0 {
			mSum := 0.0
			for _, ft := range focalTokens {
				mSum += matchMorphTokenInText(poolLow[i], ft)
			}
			if mOv := mSum / float64(len(focalTokens)); mOv > fov {
				fov = mOv
			}
		}
		focalOvList[i] = fov
		if fov >= 0.25 && pool[i].spkDate != "" && pool[i].spkDate != "未知" {
			focalDates[pool[i].spkDate] = true
		}
	}
	for i := range pool {
		hasTimeShift := pool[i].occDate != "" && pool[i].spkDate != "" && pool[i].occDate != pool[i].spkDate
		if focalOvList[i] > 0 {
			floorSc := 0.78 + 0.55*focalOvList[i]
			if floorSc > pool[i].relScore {
				pool[i].relScore = floorSc
			} else {
				pool[i].relScore += 0.35 * focalOvList[i]
			}
		}
		if hasTimeShift && focalDates[pool[i].spkDate] {
			if pool[i].relScore < 0.88 {
				pool[i].relScore = 0.88
			} else {
				pool[i].relScore += 0.25
			}
		}
	}

	sort.SliceStable(pool, func(i, j int) bool {
		if math.Abs(pool[i].relScore-pool[j].relScore) > 1e-9 {
			return pool[i].relScore > pool[j].relScore
		}
		if pool[i].kind != pool[j].kind {
			return pool[i].kind < pool[j].kind
		}
		return pool[i].seq < pool[j].seq
	})
}

// filterPoolBySubmodularMarginalGain applies focal-token IDF promotion and Top-K seat selection.
func filterPoolBySubmodularMarginalGain(pool []flatDeliveryCandidate, allTokens []string, capN int) []flatDeliveryCandidate {
	if capN < 20 || len(pool) <= memcore.TokenTier16 || len(allTokens) == 0 {
		if len(pool) > capN {
			return pool[:capN]
		}
		return pool
	}

	reweightPoolByFocalMarginalGain(pool, allTokens)

	selected := make([]flatDeliveryCandidate, 0, capN)
	for _, c := range pool {
		if len(selected) >= capN {
			break
		}
		ov := memcore.ShannonTokenOverlapSim(c.text, allTokens)
		hasTimeShift := c.occDate != "" && c.spkDate != "" && c.occDate != c.spkDate
		if len(selected) >= memcore.TokenTier16 && ov == 0 && !hasTimeShift && c.relScore < 0.55 {
			continue
		}
		selected = append(selected, c)
	}
	if len(selected) == 0 && len(pool) > 0 {
		limit := capN
		if limit > len(pool) {
			limit = len(pool)
		}
		return pool[:limit]
	}
	return selected
}

// firstSegment returns the bounded leading segment of a source text (its first
// non-empty line when present). It is the entry's self-identifying head so
// delivered entries stay resolvable back to their source items.
func firstSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if s == "" {
		return ""
	}
	return ids.Truncate(s, 160, "")
}

func isCodeBlockText(s string) bool {
	return strings.Contains(s, "```") || strings.Contains(s, "\nfunc ") || strings.Contains(s, "\ndef ") ||
		strings.Contains(s, "\nclass ") || (strings.Count(s, "{") >= 2 && strings.Count(s, "}") >= 2)
}

// BuildFlatItemDelivery transforms assembled subject bands, candidate assertions, and raw hits into
// the minimal-sufficient flat item delivery capsule:
//   - Stage 1: Collects active structured memory assertions with band-equalized decay (0.035*bandIdx + 0.015*lineInBand)
//     and folds subordinate clauses;
//   - Stage 2: Computes orthogonal local-basis residuals Text(t) \ structured memory assertion(t) across unit/parse/block
//     (strictly guarding AsOf > 0 against resurrecting historical clauses filtered out by AsOf);
//   - Stage 3: Ranks the unified pool with submodular marginal gain pruning and focal-token promotion;
//   - Stage 4: Outputs grouped by (sxo -> unit -> parse -> block) when rankGroup=true, or in pure relevance order when rankGroup=false.
func BuildFlatItemDelivery(
	bands []map[string]any,
	rawHits []map[string]any,
	usable []store.Candidate,
	query string,
	asOf int64,
	capN int,
	rankGroup bool,
) []map[string]any {
	if capN <= 0 {
		capN = 96
	} else if capN > memcore.TokenTier256 {
		capN = memcore.TokenTier256
	}
	allTokens := memcore.Tokenize(query)

	// Source head index: anchor/source trace hash -> leading segment of the source
	// text. structured memory assertion entries reference their source trace by anchor; resolving the head
	// through this index keeps delivered entries resolvable back to source items.
	hashHead := make(map[string]string, len(usable))
	for _, c := range usable {
		if strings.TrimSpace(c.Data.TraceText) == "" {
			continue
		}
		h := firstSegment(c.Data.TraceText)
		if h == "" {
			continue
		}
		for _, th := range []string{c.Data.SourceTraceHash, c.Data.AnchorTraceHash} {
			if th != "" && hashHead[th] == "" {
				hashHead[th] = h
			}
		}
	}
	resolveSrcHead := func(turnKey string) string {
		k := strings.TrimSpace(turnKey)
		if k == "" {
			return ""
		}
		if idx := strings.IndexByte(k, '#'); idx > 0 {
			k = k[:idx]
		}
		return hashHead[strings.TrimPrefix(k, "trace:")]
	}

	// Extract Cross-Encoder Rerank float scores (0..1) per assertion fact and per source turn:
	turnBestRerank := make(map[string]float64, 64)
	factBestRerank := make(map[string]float64, 128)
	for _, c := range usable {
		if c.RerankScore <= 0 || c.Data.Status == "superseded" || c.Data.Status == "retracted" {
			continue
		}
		for _, h := range []string{c.Data.SourceTraceHash, c.Data.AnchorTraceHash} {
			if h != "" {
				if c.RerankScore > turnBestRerank[h] {
					turnBestRerank[h] = c.RerankScore
				}
				sh := "trace:" + shortHash(h)
				if c.RerankScore > turnBestRerank[sh] {
					turnBestRerank[sh] = c.RerankScore
				}
			}
		}
		rendered := strings.ToLower(strings.TrimSpace(ids.RenderAssertion(c.Data.SLabel, c.Data.XLabel, c.Data.OLabel)))
		if rendered != "" && c.RerankScore > factBestRerank[rendered] {
			factBestRerank[rendered] = c.RerankScore
		}
	}

	var rawEntries []pureAssertionItem
	seenAssertion := make(map[string]int, 64)
	assertionSeq := 0
	addAssertion := func(occDate, spkDate, fact, turnKey string, baseScore float64) {
		fTrim := strings.TrimSpace(fact)
		if fTrim == "" || isInvalidOrPartDerivedAssertion(fTrim) {
			return
		}
		if rSc := factBestRerank[strings.ToLower(fTrim)]; rSc > 0 {
			baseScore += 0.65 * rSc
		}
		if turnKey != "" {
			prefixKey := turnKey
			if idx := strings.IndexByte(prefixKey, '#'); idx > 0 {
				prefixKey = prefixKey[:idx]
			}
			if trSc := turnBestRerank[prefixKey]; trSc > 0 {
				baseScore += 0.55 * trSc
			}
		}
		if occDate == "" || occDate == "unknown" {
			occDate = "未知"
		}
		if spkDate == "" || spkDate == "unknown" {
			spkDate = occDate
		}
		occDate = ReconcileClauseEventDate(fTrim, occDate, spkDate)
		key := occDate + "|" + spkDate + "|" + strings.ToLower(fTrim)
		if idx, exists := seenAssertion[key]; exists {
			if baseScore > rawEntries[idx].score {
				rawEntries[idx].score = baseScore
			}
			return
		}
		seenAssertion[key] = len(rawEntries)
		rawEntries = append(rawEntries, pureAssertionItem{
			occDate: occDate,
			spkDate: spkDate,
			fact:    fTrim,
			turnKey: turnKey,
			score:   baseScore,
			seq:     assertionSeq,
			srcHead: resolveSrcHead(turnKey),
		})
		assertionSeq++
	}

	for _, band := range bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, n := range nodes {
			if st := asString(n["status"]); st == "superseded" || st == "retracted" || st == "expired" {
				continue
			}
			rendered := strings.TrimSpace(asString(n["content"]))
			if rendered == "" {
				continue
			}
			tvf := asInt64(n["t_valid_from"])
			tsrc := asInt64(n["t_source"])
			occD := ids.FormatTimeUTC(tvf, "")
			spkD := ids.FormatTimeUTC(tsrc, "")
			if occD == "" {
				occD = asString(n["time"])
			}
			if spkD == "" {
				spkD = occD
			}
			baseSc := 0.5 + 0.5*memcore.ShannonTokenOverlapSim(rendered, allTokens)
			anchorStr := asString(n["anchor"])
			if iq := extractInlineQuotesFromLine(rendered); iq != "" {
				qSc := baseSc
				if strings.Contains(iq, "\n") {
					qSc = 1.25
				} else if qSc < 0.88 {
					qSc = 0.88
				}
				addAssertion(occD, spkD, strings.ReplaceAll(iq, "\n", " "), anchorStr, qSc)
			}
			addAssertion(occD, spkD, rendered, anchorStr, baseSc)
		}
	}

	foldedAssertion := foldSubordinatePureAssertionItems(rawEntries)

	globalSeq := 0
	var pool []flatDeliveryCandidate
	priorFactsByDate := make(map[string][]string, 32)
	var allPriorFacts []string
	coverProtectLimit := capN / 3
	if coverProtectLimit > 16 {
		coverProtectLimit = 16
	}
	if coverProtectLimit < 4 {
		coverProtectLimit = 4
	}
	for idx, e := range foldedAssertion {
		relSc := computeUnifiedCandidateRelevance(e.score, e.fact, allTokens)
		pool = append(pool, flatDeliveryCandidate{
			kind:     flatKindAssertion,
			occDate:  e.occDate,
			spkDate:  e.spkDate,
			text:     e.fact,
			locator:  e.turnKey,
			relScore: relSc,
			seq:      globalSeq,
			srcHead:  e.srcHead,
		})
		globalSeq++
		if idx < coverProtectLimit {
			priorFactsByDate[e.spkDate] = append(priorFactsByDate[e.spkDate], e.fact)
			if e.occDate != e.spkDate {
				priorFactsByDate[e.occDate] = append(priorFactsByDate[e.occDate], e.fact)
			}
			allPriorFacts = append(allPriorFacts, e.fact)
		}
	}

	// Stage 2: Extract orthogonal residuals Text(t) \ structured memory assertion(t).
	// When asOf > 0 and active structured memory assertion rows exist, do not resurrect full-turn raw supplements (as-of guard).
	deliveredResiduals := make(map[string]bool, 64)
	if asOf == 0 || len(usable) == 0 {
		// Collect turn snippets from usable candidates first (evBand equivalent), then rawHits
		seenTurnHash := make(map[string]bool, 32)
		evIdx := 0
		for _, c := range usable {
			if c.Data.Status != "active" || strings.TrimSpace(c.Data.TraceText) == "" {
				continue
			}
			th := c.Data.AnchorTraceHash
			if th == "" || seenTurnHash[th] {
				continue
			}
			seenTurnHash[th] = true
			spkD := ids.FormatTimeUTC(c.Data.TSource, "未知")
			occD := ids.FormatTimeUTC(c.Data.TValidFrom, spkD)
			localFacts := append([]string(nil), priorFactsByDate[spkD]...)
			if occD != spkD {
				localFacts = append(localFacts, priorFactsByDate[occD]...)
			}
			clauses := ExtractTurnUncoveredResidualClauses(c.Data.TraceText, localFacts, deliveredResiduals, allTokens)
			if len(clauses) == 0 {
				continue
			}
			joined := strings.Join(clauses, " ")
			occD = ReconcileClauseEventDate(joined, occD, spkD)
			priorFactsByDate[spkD] = append(priorFactsByDate[spkD], clauses...)
			allPriorFacts = append(allPriorFacts, clauses...)
			baseSc := 0.92 / (1.0 + 0.035*float64(evIdx))
			if trSc := turnBestRerank[th]; trSc > 0 {
				baseSc += 0.65 * trSc
			}
			evIdx++
			kd := flatKindUnit
			if isCodeBlockText(joined) {
				kd = flatKindBlock
			}
			pool = append(pool, flatDeliveryCandidate{
				kind:     kd,
				occDate:  occD,
				spkDate:  spkD,
				text:     joined,
				locator:  "trace:" + shortHash(th),
				relScore: computeUnifiedCandidateRelevance(baseSc, joined, allTokens),
				seq:      globalSeq,
				srcHead:  firstSegment(c.Data.TraceText),
			})
			globalSeq++
		}

		for suppIdx, hit := range rawHits {
			rawTxt := strings.TrimSpace(asString(hit["text"]))
			if rawTxt == "" {
				continue
			}
			ts := asInt64(hit["occurred_at"])
			spkD := ids.FormatTimeUTC(ts, "未知")
			occD := spkD
			localFacts := append([]string(nil), priorFactsByDate[spkD]...)
			if len(localFacts) == 0 {
				localFacts = allPriorFacts
			}
			clauses := ExtractTurnUncoveredResidualClauses(rawTxt, localFacts, deliveredResiduals, allTokens)
			if len(clauses) == 0 {
				continue
			}
			joined := strings.Join(clauses, " ")
			occD = ReconcileClauseEventDate(joined, occD, spkD)
			priorFactsByDate[spkD] = append(priorFactsByDate[spkD], clauses...)
			allPriorFacts = append(allPriorFacts, clauses...)
			baseSc := 0.90 / (1.0 + 0.05*float64(suppIdx))
			if suppIdx >= 2 {
				baseSc = 0.42 / (1.0 + 0.04*float64(suppIdx))
			}
			if rSc := asFloat(hit["_rerank_score"], 0.0); rSc > 0 {
				baseSc += 0.65 * rSc
			}
			kd := flatKindUnit
			if isCodeBlockText(joined) {
				kd = flatKindBlock
			}
			loc := fmt.Sprintf("%s#%v", asString(hit["session"]), hit["turn"])
			pool = append(pool, flatDeliveryCandidate{
				kind:     kd,
				occDate:  occD,
				spkDate:  spkD,
				text:     joined,
				locator:  loc,
				relScore: computeUnifiedCandidateRelevance(baseSc, joined, allTokens),
				seq:      globalSeq,
				srcHead:  firstSegment(rawTxt),
			})
			globalSeq++
		}
	}

	sort.SliceStable(pool, func(i, j int) bool {
		if math.Abs(pool[i].relScore-pool[j].relScore) > 1e-9 {
			return pool[i].relScore > pool[j].relScore
		}
		if pool[i].kind != pool[j].kind {
			return pool[i].kind < pool[j].kind
		}
		return pool[i].seq < pool[j].seq
	})

	fullSortedPool := append([]flatDeliveryCandidate(nil), pool...)
	if rankGroup {
		pool = filterPoolBySubmodularMarginalGain(pool, allTokens, capN)
		pool = prioritizeEvidenceInTopKPool(fullSortedPool, pool, allTokens, capN)
		sort.SliceStable(pool, func(i, j int) bool {
			if pool[i].kind != pool[j].kind {
				return pool[i].kind < pool[j].kind
			}
			return pool[i].seq < pool[j].seq
		})
	} else {
		if len(pool) > 1 {
			locRep := make(map[string]int, len(pool))
			for i := range pool {
				loc := strings.TrimSpace(pool[i].locator)
				if idx := strings.IndexByte(loc, '#'); idx > 0 {
					loc = loc[:idx]
				}
				if loc == "" {
					continue
				}
				cnt := locRep[loc]
				locRep[loc] = cnt + 1
				if cnt > 0 {
					pool[i].relScore /= 1.0 + 0.65*float64(cnt)
				}
			}
			sort.SliceStable(pool, func(i, j int) bool {
				if math.Abs(pool[i].relScore-pool[j].relScore) > 1e-9 {
					return pool[i].relScore > pool[j].relScore
				}
				if pool[i].kind != pool[j].kind {
					return pool[i].kind < pool[j].kind
				}
				return pool[i].seq < pool[j].seq
			})
		}
		if len(pool) > capN {
			pool = pool[:capN]
		}
		pool = prioritizeEvidenceInTopKPool(fullSortedPool, pool, allTokens, capN)
		sort.SliceStable(pool, func(i, j int) bool {
			if math.Abs(pool[i].relScore-pool[j].relScore) > 1e-9 {
				return pool[i].relScore > pool[j].relScore
			}
			if pool[i].kind != pool[j].kind {
				return pool[i].kind < pool[j].kind
			}
			return pool[i].seq < pool[j].seq
		})
	}

	maxRel := 0.0
	for _, c := range pool {
		if c.relScore > maxRel {
			maxRel = c.relScore
		}
	}
	if maxRel <= 0 {
		maxRel = 1.0
	}
	out := make([]map[string]any, 0, len(pool))
	for idx, c := range pool {
		occ := c.occDate
		if occ == "" {
			occ = "未知"
		}
		spk := c.spkDate
		if spk == "" {
			spk = occ
		}
		contentLine := "发生:" + occ + " · 叙述:" + spk + " ｜ " + memcore.EscapeEngineChannel(c.text)
		// Report each item's own normalized relevance. The previous value was a pure function of
		// delivery rank (1 - idx*0.008), a rank-index score that leaked into band/tail comparisons.
		sc := math.Max(0.01, math.Round((c.relScore/maxRel)*10000)/10000)
		itemID := c.locator
		if itemID == "" {
			itemID = fmt.Sprintf("item:%d", idx+1)
		}
		node := map[string]any{
			"id":         itemID,
			"kind":       "flat_item",
			"content":    contentLine,
			"anchor":     c.locator,
			"src_head":   c.srcHead,
			"confidence": 1.0,
			"status":     "active",
			"time":       occ,
			"score":      sc,
		}
		out = append(out, map[string]any{
			"id":          itemID,
			"kind":        "item",
			"title":       contentLine,
			"subject":     itemID,
			"time_label":  occ,
			"space_label": "item",
			"score":       sc,
			"chains":      []any{},
			"nodes":       []map[string]any{node},
		})
	}
	return out
}

// buildPremiseAttributionNote computes the 0-LLM graph-topology premise & attribution verification
// note for the primary subject band (delegating 1:1 to memcore.ComputeBandPremiseAttributionNote · U28).
func buildPremiseAttributionNote(query string, bands []map[string]any) string {
	if len(bands) == 0 || strings.TrimSpace(query) == "" {
		return ""
	}
	qTokens := memcore.Tokenize(query)
	if len(qTokens) < 2 {
		return ""
	}
	primSubj := strings.ToLower(strings.TrimSpace(asString(bands[0]["subject"])))
	if primSubj == "" || primSubj == "xo" || primSubj == "misc" {
		return ""
	}
	isCueMatched := strings.Contains(strings.ToLower(query), primSubj)
	var nonSubjCues []string
	for _, tk := range qTokens {
		tl := strings.ToLower(strings.TrimSpace(tk))
		if tl == "" || strings.Contains(primSubj, tl) {
			continue
		}
		if memcore.IsHighGainToken(tl) {
			nonSubjCues = append(nonSubjCues, tl)
		}
	}
	if len(nonSubjCues) == 0 {
		return ""
	}
	profiles := make([]memcore.BandConceptProfile, 0, len(bands))
	for bi, b := range bands {
		subj := strings.TrimSpace(asString(b["subject"]))
		if subj == "" || subj == "XO" || subj == "misc" {
			continue
		}
		nodes, _ := b["nodes"].([]map[string]any)
		declHits := map[string]bool{}
		maxRowHits := 0
		multiHitRows := 0
		for _, n := range nodes {
			if asString(n["status"]) != "active" {
				continue
			}
			txt := strings.ToLower(asString(n["content"]))
			rowHits := 0
			for _, cue := range nonSubjCues {
				if strings.Contains(txt, cue) || memcore.MatchTokenOrStem(txt, cue) > 0 {
					declHits[cue] = true
					rowHits++
				}
			}
			if rowHits > maxRowHits {
				maxRowHits = rowHits
			}
			if rowHits >= 2 {
				multiHitRows++
			}
		}
		profiles = append(profiles, memcore.BandConceptProfile{
			SubjectLabel: subj,
			IsCueMatched: bi == 0 && isCueMatched,
			RowCount:     len(nodes),
			DeclHits:     declHits,
			MaxRowHits:   maxRowHits,
			MultiHitRows: multiHitRows,
		})
	}
	return memcore.ComputeBandPremiseAttributionNote(nonSubjCues, profiles, false)
}
