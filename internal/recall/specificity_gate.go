// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// SubjectCandidate represents an active subject band in the graph with pre-tokenized
// lexical features and endogenous corpus specificity weights.
type SubjectCandidate struct {
	Label        string
	Clean        string
	Core         string
	Body         string
	MinCreatedAt int64
	Tokens       []string
	TokenWeights map[string]float64
	TokenDF      map[string]int
	BodyTokens   map[string]bool
	MinTitleDF   int
	TotalWeight  float64
}

// EffectiveShannonBits computes the hardened Shannon self-information (in bits) of s
// for recall/hook gating, closing adversarial vulnerabilities V1 (non-carrier punctuation/whitespace
// parasitic bits like "--------" = 8.0b) and V2 (0th-order Markov stutter flood like "aaaaaaaaaa" = 20.0b):
//   - Non-alphanumeric runes (whitespace, punctuation, symbols, combining marks) contribute 0 bits;
//   - Consecutive identical runes repeated k >= 3 times decay exponentially by 0.25^(k-2);
//   - Strings with zero letter/digit carrier runes return 0.
func EffectiveShannonBits(s string) float64 {
	var totalBits float64
	var blockBits float64
	hasAlnum := false
	var prevRune rune
	runLen := 0
	var buf [64]rune
	var inTandem [64]bool
	n := 0

	flushBlock := func() {
		if n == 0 {
			return
		}
		if n >= 4 {
			for i := 0; i < n; i++ {
				inTandem[i] = false
			}
			for p := 1; p <= 4; p++ {
				for start := 0; start <= n-3*p; start++ {
					match3 := true
					for k := start + p; k < start+3*p; k++ {
						if buf[k] != buf[start+(k-start)%p] {
							match3 = false
							break
						}
					}
					if match3 {
						end := start + 3*p
						for end < n && buf[end] == buf[start+(end-start)%p] {
							end++
						}
						for k := start; k < end; k++ {
							inTandem[k] = true
						}
					}
				}
			}
			tandemCnt := 0
			for i := 0; i < n; i++ {
				if inTandem[i] {
					tandemCnt++
				}
			}
			ratio := float64(tandemCnt) / float64(n)
			if ratio >= 0.60 {
				d := 1.0 - ratio
				blockBits *= d * d * 0.25
			}
		}
		totalBits += blockBits
		blockBits = 0
		n = 0
	}

	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			continue
		}
		hasAlnum = true
		lowR := unicode.ToLower(r)
		buf[n] = lowR
		n++
		if lowR == prevRune {
			runLen++
		} else {
			prevRune = lowR
			runLen = 1
		}
		rb := memcore.RuneShannonBits(r)
		if runLen >= 3 {
			rb *= math.Pow(0.25, float64(runLen-2))
		}
		blockBits += rb
		if n == 64 {
			flushBlock()
		}
	}
	if !hasAlnum {
		return 0
	}
	flushBlock()
	return totalBits
}

// FastLongestVerbatimSubspan finds the highest-entropy contiguous substring (>=8.0 Shannon bits)
// of anchor that occurs verbatim in turnText using monotonic binary search over right endpoints,
// reducing worst-case time complexity from O(N^3) (5.65s on 1500 runes) to O(N log N) (<2ms).
func FastLongestVerbatimSubspan(turnText, anchor string) string {
	ar := []rune(strings.TrimSpace(anchor))
	n := len(ar)
	if n == 0 || turnText == "" {
		return ""
	}
	best := ""
	bestBits := 0.0
	for i := 0; i < n; i++ {
		if i+1 <= n && !strings.Contains(turnText, string(ar[i:i+1])) {
			continue
		}
		lo, hi := i+1, n
		maxEnd := i
		for lo <= hi {
			mid := (lo + hi) / 2
			if strings.Contains(turnText, string(ar[i:mid])) {
				maxEnd = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if maxEnd <= i {
			continue
		}
		cand := strings.TrimSpace(string(ar[i:maxEnd]))
		bits := EffectiveShannonBits(cand)
		if bits >= memcore.AtomicConceptEntropyBits && bits > bestBits {
			best = cand
			bestBits = bits
		}
	}
	return best
}

// StripTitleDateAndAuthorSuffix strips trailing archival date tags from a band title.
func StripTitleDateAndAuthorSuffix(s string) string {
	for _, sep := range []string{"（20", "(20", " · 20", "· 20", "·20"} {
		if idx := strings.Index(s, sep); idx > 0 {
			s = strings.TrimSpace(s[:idx])
		}
	}
	return s
}

// StripTitleParenthetical removes parenthetical qualifiers while preserving the head concept.
func StripTitleParenthetical(s string) string {
	var out []rune
	depth := 0
	for _, r := range s {
		if r == '(' || r == '（' || r == '[' || r == '【' {
			depth++
			continue
		}
		if r == ')' || r == '）' || r == ']' || r == '】' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			out = append(out, r)
		}
	}
	res := strings.TrimSpace(string(out))
	if EffectiveShannonBits(res) < 0.70*memcore.AtomicConceptEntropyBits {
		return s
	}
	return res
}

// CompactCueSubjectLabel produces a compact cue expansion label from a probed subject.
// For long uninterrupted non-ASCII compound titles (> 12.5*AtomicConceptEntropyBits = 100 Shannon bits),
// it includes the full compound FIRST so SearchSeeds's exact/substring leg matches the exact subject node (0.90),
// followed by a 5.0*AtomicConceptEntropyBits (40-bit) trailing anchor.
func CompactCueSubjectLabel(s, workspace string) string {
	core := StripTitleParenthetical(StripTitleDateAndAuthorSuffix(s))
	lowerWS := strings.ToLower(strings.TrimSpace(workspace))
	var words []string
	for _, w := range strings.Fields(core) {
		if lowerWS != "" && strings.EqualFold(w, lowerWS) {
			continue
		}
		nonASCII := true
		for _, ch := range w {
			if ch <= 127 {
				nonASCII = false
				break
			}
		}
		if nonASCII && memcore.StringShannonBits(w) > 12.5*memcore.AtomicConceptEntropyBits {
			var acc float64
			suffixStart := 0
			for idx := len(w); idx > 0; {
				r, size := utf8DecodeLastRune(w[:idx])
				idx -= size
				acc += memcore.RuneShannonBits(r)
				if acc >= 5.0*memcore.AtomicConceptEntropyBits {
					suffixStart = idx
					break
				}
			}
			words = append(words, w, w[suffixStart:])
		} else {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return core
	}
	return strings.Join(words, " ")
}

func utf8DecodeLastRune(s string) (rune, int) {
	for i := len(s) - 1; i >= 0; i-- {
		if (s[i] & 0xC0) != 0x80 {
			for _, r := range s[i:] {
				return r, len(s) - i
			}
		}
	}
	return 0, 0
}

// ExtractStructuralPaths extracts up to maxPaths structural file/directory coordinates
// from text (rejecting URLs, protocol-relative //localhost fragments, and ambient root .md files).
func ExtractStructuralPaths(text string, maxPaths int) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("()（）[]【】<>\"'`:,，；;。!?", r)
	}) {
		if IsStructuralFilePath(tok) {
			low := strings.ToLower(tok)
			if !seen[low] {
				seen[low] = true
				out = append(out, tok)
				if len(out) >= maxPaths {
					break
				}
			}
		}
	}
	return out
}

// IsStructuralFilePath reports whether token is a genuine ASCII file/directory path coordinate.
func IsStructuralFilePath(token string) bool {
	if len(token) < 5 || strings.HasPrefix(token, "//") || strings.HasPrefix(token, "trace:") ||
		strings.HasPrefix(token, "http") || strings.ContainsAny(token, "@&<>=") {
		return false
	}
	for _, r := range token {
		if r > 127 {
			return false
		}
	}
	slashIdx := strings.IndexByte(token, '/')
	dotIdx := strings.LastIndexByte(token, '.')
	if slashIdx < 0 && strings.Count(token, ".") >= 2 {
		return false
	}
	if slashIdx < 0 {
		lowerTok := strings.ToLower(token)
		if strings.HasSuffix(lowerTok, ".md") || strings.HasSuffix(lowerTok, ".db") {
			return false
		}
	}
	if dotIdx > 2 && dotIdx < len(token)-1 && dotIdx > slashIdx {
		ext := token[dotIdx+1:]
		if len(ext) >= 2 && len(ext) <= 4 {
			allAlpha := true
			for _, r := range ext {
				if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
					allAlpha = false
					break
				}
			}
			if allAlpha {
				return true
			}
		}
	}
	if strings.HasPrefix(token, "~/") || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "/") {
		if slashIdx >= 0 && slashIdx < len(token)-1 {
			hasLetter := false
			hasUpper := false
			for _, r := range token {
				if r >= 'a' && r <= 'z' {
					hasLetter = true
				} else if r >= 'A' && r <= 'Z' {
					hasUpper = true
				}
			}
			if hasLetter && !hasUpper {
				return true
			}
		}
	}
	return false
}

// ExpandScriptSubtokens extracts script-transition subtokens and CJK 3/4-gram windows
// so FTS seed lookup can bridge unsegmented CJK runs.
func ExpandScriptSubtokens(prompt string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		ls := strings.ToLower(s)
		if s == "" || seen[ls] {
			return
		}
		seen[ls] = true
		out = append(out, s)
	}
	for _, tok := range strings.FieldsFunc(prompt, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("()（）[]【】<>\"'`:,，；;。!?！？/\\", r)
	}) {
		if memcore.StringShannonBits(tok) < 4.5 {
			continue
		}
		runes := []rune(tok)
		start := 0
		for i := 1; i <= len(runes); i++ {
			if i == len(runes) || (runes[i] <= 127) != (runes[start] <= 127) {
				seg := string(runes[start:i])
				segRunes := runes[start:i]
				if start > 0 || i < len(runes) {
					if segRunes[0] <= 127 && len(segRunes) >= 2 {
						add(seg)
					} else if segRunes[0] > 127 && len(segRunes) >= 3 && len(segRunes) <= 6 {
						add(seg)
					}
				}
				start = i
			}
		}
		var nonASCII []rune
		for _, r := range runes {
			if r > 127 && unicode.IsLetter(r) {
				nonASCII = append(nonASCII, r)
			} else {
				if len(nonASCII) >= 5 && len(nonASCII) <= 14 {
					add(string(nonASCII[:4]))
					for k := 0; k+3 <= len(nonASCII) && len(out) < 10; k++ {
						add(string(nonASCII[k : k+3]))
					}
				}
				nonASCII = nonASCII[:0]
			}
		}
		if len(nonASCII) >= 5 && len(nonASCII) <= 14 {
			add(string(nonASCII[:4]))
			for k := 0; k+3 <= len(nonASCII) && len(out) < 10; k++ {
				add(string(nonASCII[k : k+3]))
			}
		}
		if len(out) >= 10 {
			break
		}
	}
	return out
}

func stripWorkspaceTokens(text, workspace string) string {
	lower := strings.ToLower(text)
	for _, repo := range strings.Fields(workspace) {
		rl := strings.ToLower(strings.TrimSpace(repo))
		if rl == "" {
			continue
		}
		lower = strings.ReplaceAll(lower, rl, " ")
		if dash := strings.IndexByte(rl, '-'); dash >= 3 {
			prefix := rl[:dash+1]
			for _, tok := range strings.FieldsFunc(lower, func(r rune) bool {
				return !(unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-')
			}) {
				if strings.HasPrefix(tok, prefix) && len(tok) > len(prefix)+1 {
					lower = strings.ReplaceAll(lower, tok, " ")
				}
			}
		}
	}
	return strings.TrimSpace(lower)
}

// extractLexicalFeatures extracts the 4-channel features used by memcore.FeatureHashVec:
//   - "id:<alnum>" for alphanumeric identifiers containing digits (e.g. "g5", "f0", "v2")
//   - "w3:<word>" for 3-letter ASCII words (e.g. "roi", "mcp", "sql", "api")
//   - "w:<word>" for >=4-letter ASCII words (plus stem)
//   - "b:<2gram>" and "t:<3gram>" for continuous ideographic runs
func extractLexicalFeatures(text string) []string {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	runes := []rune(lower)
	n := len(runes)
	for i := 0; i < n; {
		if !(unicode.IsLetter(runes[i]) || unicode.IsNumber(runes[i])) {
			i++
			continue
		}
		isASCII := runes[i] <= 127
		j := i + 1
		for j < n && (unicode.IsLetter(runes[j]) || unicode.IsNumber(runes[j])) && (runes[j] <= 127) == isASCII {
			j++
		}
		seg := runes[i:j]
		if isASCII {
			hasDigit, hasAlpha := false, false
			for _, r := range seg {
				if unicode.IsNumber(r) {
					hasDigit = true
				} else if unicode.IsLetter(r) {
					hasAlpha = true
				}
			}
			s := string(seg)
			if hasDigit && hasAlpha && len(seg) >= 2 {
				add("id:" + s)
			} else if hasDigit && !hasAlpha && len(seg) >= 3 && len(seg) <= 6 {
				add("num:" + s)
			} else if hasAlpha && len(seg) == 2 {
				add("w2:" + s)
			} else if hasAlpha && len(seg) == 3 {
				add("w3:" + s)
			} else if hasAlpha && len(seg) >= 4 {
				add("w:" + s)
			}
		} else {
			if len(seg) == 1 {
				add("c1:" + string(seg))
			} else {
				for k := 0; k+2 <= len(seg); k++ {
					add("b:" + string(seg[k:k+2]))
				}
				for k := 0; k+3 <= len(seg); k++ {
					add("t:" + string(seg[k:k+3]))
				}
			}
		}
		i = j
	}
	return out
}

// computeBipartiteMaskTopology projects matched features onto BOTH the candidate title `cleanTitle`
// and the query `cleanQuery`, enforcing Bipartite Shannon Self-Information Conservation (zero character-count rules):
//   - effCovBits   = min(covBits_T, covBits_Q)   (measured in Shannon bits via memcore.RuneShannonBits)
//   - totalBits    = totBits_T
//   - effSpanCount = min(spanCount_T, spanCount_Q)
//   - ideoCovBits, ideoTotalBits, ideoSpanCount: Shannon bit coverage on the continuous non-ASCII stem.
func computeBipartiteMaskTopology(qSet map[string]bool, cleanQuery, cleanTitle string) (
	effCovBits, totalBits float64, effSpanCount int, bitCov float64,
	ideoCovBits, ideoTotalBits float64, ideoSpanCount int, ideoBitCov float64,
) {
	tFeats := extractLexicalFeatures(cleanTitle)
	matchedSet := make(map[string]bool, len(tFeats))
	for _, f := range tFeats {
		if qSet[f] {
			matchedSet[f] = true
		}
	}
	if len(matchedSet) == 0 {
		for _, r := range cleanTitle {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				rb := memcore.RuneShannonBits(r)
				totalBits += rb
				if r > 127 {
					ideoTotalBits += rb
				}
			}
		}
		return
	}

	markMask := func(s string) (covAll, totAll float64, spansAll int, covIdeo, totIdeo float64, spansIdeo int) {
		runes := []rune(strings.ToLower(strings.TrimSpace(s)))
		n := len(runes)
		if n == 0 {
			return
		}
		covered := make([]bool, n)
		for i := 0; i < n; {
			if !(unicode.IsLetter(runes[i]) || unicode.IsNumber(runes[i])) {
				i++
				continue
			}
			isASCII := runes[i] <= 127
			j := i + 1
			for j < n && (unicode.IsLetter(runes[j]) || unicode.IsNumber(runes[j])) && (runes[j] <= 127) == isASCII {
				j++
			}
			seg := runes[i:j]
			if isASCII {
				hasDigit, hasAlpha := false, false
				for _, r := range seg {
					if unicode.IsNumber(r) {
						hasDigit = true
					} else if unicode.IsLetter(r) {
						hasAlpha = true
					}
				}
				str := string(seg)
				var key string
				if hasDigit && hasAlpha && len(seg) >= 2 {
					key = "id:" + str
				} else if hasDigit && !hasAlpha && len(seg) >= 3 && len(seg) <= 6 {
					key = "num:" + str
				} else if hasAlpha && len(seg) == 2 {
					key = "w2:" + str
				} else if hasAlpha && len(seg) == 3 {
					key = "w3:" + str
				} else if hasAlpha && len(seg) >= 4 {
					key = "w:" + str
				}
				if key != "" && matchedSet[key] {
					if strings.HasPrefix(key, "w2:") {
						covered[i] = true
					} else {
						for k := i; k < j; k++ {
							covered[k] = true
						}
					}
				}
			} else {
				for k := 0; k+2 <= len(seg); k++ {
					if matchedSet["b:"+string(seg[k:k+2])] {
						covered[i+k] = true
						covered[i+k+1] = true
					}
				}
			}
			i = j
		}
		inAll, inIdeo := false, false
		for i, r := range runes {
			if !(unicode.IsLetter(r) || unicode.IsNumber(r)) {
				inAll = false
				inIdeo = false
				continue
			}
			rb := memcore.RuneShannonBits(r)
			totAll += rb
			if covered[i] {
				covAll += rb
				if !inAll {
					spansAll++
					inAll = true
				}
			} else {
				inAll = false
			}
			if r > 127 {
				totIdeo += rb
				if covered[i] {
					covIdeo += rb
					if !inIdeo {
						spansIdeo++
						inIdeo = true
					}
				} else {
					inIdeo = false
				}
			} else {
				inIdeo = false
			}
		}
		return
	}

	tCov, tTot, tSpans, tIdeoCov, tIdeoTot, tIdeoSpans := markMask(cleanTitle)
	qCov, _, qSpans, qIdeoCov, _, qIdeoSpans := markMask(cleanQuery)

	effCovBits = math.Min(tCov, qCov)
	totalBits = tTot
	effSpanCount = tSpans
	if effCovBits <= 30.0 && qSpans < effSpanCount {
		effSpanCount = qSpans
	}
	if totalBits > 0 {
		bitCov = effCovBits / totalBits
	}
	ideoCovBits = math.Min(tIdeoCov, qIdeoCov)
	ideoTotalBits = tIdeoTot
	ideoSpanCount = tIdeoSpans
	if ideoCovBits <= 30.0 && qIdeoSpans < ideoSpanCount {
		ideoSpanCount = qIdeoSpans
	}
	if ideoTotalBits > 0 {
		ideoBitCov = ideoCovBits / ideoTotalBits
	}
	return
}

var (
	globalCorpusTokenDF   map[string]int
	globalCorpusTotalDocs int
	// globalCorpusDFMu guards the online document-frequency table: the recall paths
	// read it while the efficacy pass republishes it, and Go maps must never be read
	// concurrently with a write. Published tables are replaced, never mutated, so a
	// snapshot may be read after the lock is released.
	globalCorpusDFMu sync.RWMutex
)

// globalCorpusDFValue returns one feature's online document frequency (0 when no
// table is installed).
func globalCorpusDFValue(feature string) int {
	globalCorpusDFMu.RLock()
	defer globalCorpusDFMu.RUnlock()
	return globalCorpusTokenDF[feature]
}

// globalCorpusDFSnapshot returns the published table, its total document count,
// and whether an online table is installed at all.
func globalCorpusDFSnapshot() (table map[string]int, totalDocs int, installed bool) {
	globalCorpusDFMu.RLock()
	defer globalCorpusDFMu.RUnlock()
	return globalCorpusTokenDF, globalCorpusTotalDocs, globalCorpusTokenDF != nil
}

// globalCorpusDFProbe returns one feature's frequency plus the totals; installed
// reports whether an online table exists, and totalDocs falls back to the table
// size when the stored count is non-positive.
func globalCorpusDFProbe(feature string) (df int, totalDocs int, installed bool) {
	globalCorpusDFMu.RLock()
	defer globalCorpusDFMu.RUnlock()
	if globalCorpusTokenDF == nil {
		return 0, globalCorpusTotalDocs, false
	}
	totalDocs = globalCorpusTotalDocs
	if totalDocs <= 0 {
		totalDocs = len(globalCorpusTokenDF)
	}
	return globalCorpusTokenDF[feature], totalDocs, true
}

// publishGlobalCorpusDF installs a freshly built online DF table.
func publishGlobalCorpusDF(df map[string]int, totalDocs int) {
	globalCorpusDFMu.Lock()
	globalCorpusTokenDF = df
	globalCorpusTotalDocs = totalDocs
	globalCorpusDFMu.Unlock()
}

// PreindexSubjectCandidates pre-computes the lexical feature sets and hub-damped
// specificity weights (1 / (1 + ln(1 + df))) across all active subjects in a single O(N) pass (<0.5ms).
func PreindexSubjectCandidates(raw []SubjectCandidate) []SubjectCandidate {
	if len(raw) == 0 {
		return nil
	}
	df := make(map[string]int, len(raw)*4)
	out := make([]SubjectCandidate, len(raw))
	for i, item := range raw {
		cl := StripTitleDateAndAuthorSuffix(item.Label)
		cr := StripTitleParenthetical(cl)
		feats := extractLexicalFeatures(cl)
		for _, f := range feats {
			df[f]++
		}
		var bodyToks map[string]bool
		if item.Body != "" {
			bFeats := extractLexicalFeatures(item.Body)
			if len(bFeats) > 0 {
				bodyToks = make(map[string]bool, len(bFeats))
				for _, bf := range bFeats {
					if !strings.HasPrefix(bf, "c1:") {
						bodyToks[bf] = true
					}
				}
			}
		}
		out[i] = SubjectCandidate{
			Label:        item.Label,
			Clean:        cl,
			Core:         cr,
			Body:         item.Body,
			MinCreatedAt: item.MinCreatedAt,
			Tokens:       feats,
			BodyTokens:   bodyToks,
		}
	}
	publishGlobalCorpusDF(df, len(out))
	nDocs := float64(max(len(out), 1))
	for i := range out {
		weights := make(map[string]float64, len(out[i].Tokens))
		tokDF := make(map[string]int, len(out[i].Tokens))
		minDF := 999999
		totalW := 0.0
		for _, f := range out[i].Tokens {
			dInt := df[f]
			if dInt <= 0 {
				dInt = 1
			}
			tokDF[f] = dInt
			if !strings.HasPrefix(f, "c1:") && dInt < minDF {
				minDF = dInt
			}
			d := float64(dInt)
			// Endogenous IDF * Inverse-DF Hub Specificity Damping: 1 / (1 + ln(1 + d))
			idf := 1.0 + math.Log((nDocs+1.0)/(d+1.0))
			hubDamp := 1.0 / (1.0 + math.Log(1.0+d))
			w := idf * hubDamp
			if dInt == 1 {
				w *= 1.65 // Unique terminal boost (DF == 1 across corpus)
			} else if dInt == 2 {
				w *= 1.25
			}
			if strings.HasPrefix(f, "id:") {
				w *= 2.2
			} else if strings.HasPrefix(f, "t:") {
				w *= 1.35
			} else if strings.HasPrefix(f, "w:") {
				w *= 1.40
			} else if strings.HasPrefix(f, "w3:") {
				w *= 1.15
			}
			weights[f] = w
			totalW += w
		}
		out[i].TokenWeights = weights
		out[i].TokenDF = tokDF
		out[i].MinTitleDF = minDF
		out[i].TotalWeight = totalW
	}
	return out
}

// ProbeSubjectsBySpecificity awakens up to maxHits active subject labels in <0.05ms
// using the pre-indexed lexical feature subspace and hub-damped specificity.
func ProbeSubjectsBySpecificity(subjects []SubjectCandidate, prompt, workspace string, promptAt int64, maxHits int) []string {
	if len(subjects) == 0 || maxHits <= 0 {
		return nil
	}
	cleanQ := stripWorkspaceTokens(prompt, workspace)
	qBits := EffectiveShannonBits(cleanQ)
	if qBits < 0.70*memcore.AtomicConceptEntropyBits {
		return nil
	}
	qFeats := extractLexicalFeatures(cleanQ)
	if len(qFeats) == 0 {
		return nil
	}
	qSet := make(map[string]bool, len(qFeats))
	qHasID := false
	for _, f := range qFeats {
		qSet[f] = true
		if strings.HasPrefix(f, "id:") {
			qHasID = true
		}
	}
	cutoff := promptAt + 5

	type hit struct {
		label string
		score float64
	}
	var matches []hit

	for _, subj := range subjects {
		if promptAt > 0 && subj.MinCreatedAt > cutoff {
			continue
		}
		feats := subj.Tokens
		weights := subj.TokenWeights
		if len(feats) == 0 {
			cl := StripTitleDateAndAuthorSuffix(subj.Label)
			feats = extractLexicalFeatures(stripWorkspaceTokens(cl, workspace))
			weights = make(map[string]float64, len(feats))
			for _, f := range feats {
				weights[f] = 1.0
			}
		}

		matchedW := 0.0
		totalW := 0.0
		matchedBigrams := 0
		matchedTrigrams := 0
		matchedASCII := 0
		unmatchedASCII := 0
		hasTitleNonASCII := false
		matchedNonASCII := false
		hasIDMatch := false
		minMatchedDF := 999999
		rareMatches := 0

		lowerWS := strings.ToLower(strings.TrimSpace(workspace))
		for _, f := range feats {
			if lowerWS != "" && (f == "w:"+lowerWS || strings.Contains(lowerWS, strings.TrimPrefix(f, "w:"))) {
				continue
			}
			w := weights[f]
			if w <= 0 {
				w = 1.0
			}
			if !strings.HasPrefix(f, "t:") {
				totalW += w
			}
			isHit := qSet[f]
			if isHit && !strings.HasPrefix(f, "c1:") && !strings.HasPrefix(f, "t:") {
				if d := subj.TokenDF[f]; d > 0 {
					if d < minMatchedDF {
						minMatchedDF = d
					}
					if d <= 3 {
						rareMatches++
					}
				}
			}
			if strings.HasPrefix(f, "id:") || strings.HasPrefix(f, "num:") || strings.HasPrefix(f, "w2:") || strings.HasPrefix(f, "w3:") || strings.HasPrefix(f, "w:") {
				if isHit {
					matchedW += w
					matchedASCII++
					if strings.HasPrefix(f, "id:") {
						hasIDMatch = true
					}
				} else {
					unmatchedASCII++
				}
			} else {
				hasTitleNonASCII = true
				if isHit && !strings.HasPrefix(f, "c1:") {
					if strings.HasPrefix(f, "t:") {
						matchedW += w * 0.55
						matchedTrigrams++
					} else {
						matchedW += w
						matchedBigrams++
					}
					matchedNonASCII = true
				}
			}
		}

		if totalW <= 0 {
			continue
		}
		if matchedW == 0 && !(qHasID && len(subj.BodyTokens) > 0) {
			continue
		}

		// Co-occurrence Resonance (body token co-activation):
		// Count how many query features co-occur in the subject's object/predicate body (`subj.BodyTokens`).
		bodyHits := 0
		bodyIDHit := false
		if len(subj.BodyTokens) > 0 {
			for f := range qSet {
				if subj.BodyTokens[f] {
					if strings.HasPrefix(f, "id:") {
						bodyIDHit = true
						bodyHits += 2
					} else {
						bodyHits++
					}
				}
			}
		}

		// Dual-script orthogonality:
		// 1) If title has both ASCII/numeric qualifiers and non-ASCII ideographs, do not fire on an incomplete ASCII match when ideographs have 0 overlap;
		// 2) If title has >=4 non-ASCII ideographs and ASCII qualifiers, do not fire on ASCII-only prefix match when ideographs have 0 overlap (`!matchedNonASCII`);
		// 3) If title has ASCII/numeric qualifiers (`unmatchedASCII > 0`) and NONE of them matched (`matchedASCII == 0`), require strong ideographic coverage (>=0.48).
		if unmatchedASCII > 0 && hasTitleNonASCII && (!matchedNonASCII || (matchedTrigrams == 0 && matchedBigrams <= 1 && !hasIDMatch && !bodyIDHit)) {
			continue
		}
		if matchedASCII == 0 && unmatchedASCII > 0 && (matchedW/totalW) < 0.48 && !bodyIDHit {
			continue
		}
		if !hasTitleNonASCII && unmatchedASCII > 0 && matchedASCII == 0 {
			continue
		}

		cleanSubj := stripWorkspaceTokens(subj.Core, workspace)
		if memcore.StringShannonBits(cleanSubj) < 0.70*memcore.AtomicConceptEntropyBits {
			cleanSubj = stripWorkspaceTokens(subj.Clean, workspace)
		}
		covBits, totBits, spanCount, bitCov, ideoCovBits, ideoTotBits, ideoSpans, ideoBitCov := computeBipartiteMaskTopology(qSet, cleanQ, cleanSubj)
		// Veto an ASCII-only match on an ideographic-stemmed title ONLY when the matched ASCII anchor is itself a
		// corpus hub (DF > 4) rather than a specific topical term.
		if hasTitleNonASCII && !matchedNonASCII && ideoTotBits >= 20.0 && minMatchedDF > 4 && !hasIDMatch && !bodyIDHit {
			continue
		}

		// Shannon Self-Information Stem Conservation (measured strictly in Shannon bits):
		if ideoTotBits >= 50.0 && ideoSpans <= 1 && !(matchedASCII >= 1 && unmatchedASCII == 0 && minMatchedDF <= 3 && ideoBitCov >= 0.40) && ideoBitCov < 0.45 && bitCov < 0.58 && !hasIDMatch && !bodyIDHit {
			continue
		}
		if qBits >= 200.0 && ideoTotBits >= 40.0 && ideoCovBits <= 20.0 && matchedTrigrams == 0 && rareMatches == 0 && ideoSpans <= 1 && !hasIDMatch && !bodyIDHit {
			continue
		}
		if qBits >= 150.0 && spanCount <= 1 && covBits <= 30.0 && totBits >= 46.0 && (rareMatches == 0 || (qBits >= 240.0 && minMatchedDF > 1)) && !hasIDMatch && !bodyIDHit {
			continue
		}
		if totBits <= 10.0 && qBits >= 64.0 && !hasIDMatch {
			continue
		}

		// Sublinear BM25-style specificity score applies when >= 2 disjoint spans
		// (`spanCount >= 2`) co-occur, or when a >= 46-bit contiguous phrase / rare terminal hits.
		specRecall := matchedW / totalW
		if (spanCount >= 2 && covBits >= 24.0 && (ideoTotBits < 40.0 || ideoSpans >= 2 || ideoCovBits >= 36.0)) || covBits >= 46.0 || (rareMatches >= 1 && covBits >= 24.0 && (qBits < 240.0 || minMatchedDF == 1 || spanCount >= 2)) {
			bm25Spec := matchedW / (1.4 + 0.35*totalW)
			if bm25Spec > specRecall {
				specRecall = bm25Spec
			}
		} else if spanCount <= 1 && totBits >= 56.0 && bitCov < 0.45 && !hasIDMatch && !bodyIDHit {
			continue
		}

		if matchedTrigrams == 0 && rareMatches == 0 && matchedASCII == 0 && !hasIDMatch && !bodyIDHit && totBits >= 46.0 && (qBits >= 110.0 || bitCov < 0.65) {
			continue
		}

		longOK := qBits < 160.0 || (matchedTrigrams >= 1 && (spanCount >= 2 || covBits >= 36.0 || bitCov >= 0.45 || minMatchedDF == 1)) ||
			(rareMatches >= 1 && spanCount >= 2 && covBits >= 24.0) ||
			(matchedASCII >= 1 && (matchedBigrams >= 2 || (matchedBigrams >= 1 && matchedASCII >= 2)) && minMatchedDF <= 4) ||
			hasIDMatch || bodyIDHit || bitCov >= 0.55

		passes := (longOK && specRecall >= 0.28 && (spanCount >= 2 || bitCov >= 0.42 || (matchedASCII >= 2 && matchedBigrams >= 1 && unmatchedASCII == 0) || (matchedASCII >= 1 && unmatchedASCII == 0 && (!hasTitleNonASCII || matchedNonASCII)))) ||
			(hasIDMatch && (unmatchedASCII == 0 || (matchedBigrams >= 1 && bodyHits >= 2)) && (specRecall >= 0.18 || matchedNonASCII)) ||
			(bodyIDHit && (matchedBigrams >= 2 || (matchedBigrams >= 1 && matchedASCII >= 1)) && bodyHits >= 4) ||
			(matchedASCII >= 1 && unmatchedASCII == 0 && (!hasTitleNonASCII || matchedNonASCII) && specRecall >= 0.26 && (spanCount >= 2 || bitCov >= 0.36))

		if passes {
			sc := matchedW*1.6 + specRecall*3.0 + float64(rareMatches)*1.8 + float64(spanCount)*0.8 + (covBits/memcore.AtomicConceptEntropyBits)*0.35 + float64(matchedASCII)*0.8
			matches = append(matches, hit{label: subj.Label, score: sc})
		}
	}

	if len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})
	if len(matches) > maxHits {
		matches = matches[:maxHits]
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.label)
	}
	return out
}

func iterBandNodes(b map[string]any, fn func(map[string]any)) {
	if nodes, ok := b["nodes"].([]map[string]any); ok {
		for _, n := range nodes {
			fn(n)
		}
	} else if nodesAny, ok := b["nodes"].([]any); ok {
		for _, nv := range nodesAny {
			if n, ok := nv.(map[string]any); ok {
				fn(n)
			}
		}
	}
	if chains, ok := b["chains"].([]map[string]any); ok {
		for _, chain := range chains {
			if nodes, ok := chain["nodes"].([]map[string]any); ok {
				for _, n := range nodes {
					fn(n)
				}
			} else if nodesAny, ok := chain["nodes"].([]any); ok {
				for _, nv := range nodesAny {
					if n, ok := nv.(map[string]any); ok {
						fn(n)
					}
				}
			}
		}
	} else if chainsAny, ok := b["chains"].([]any); ok {
		for _, cv := range chainsAny {
			if chain, ok := cv.(map[string]any); ok {
				if nodes, ok := chain["nodes"].([]map[string]any); ok {
					for _, n := range nodes {
						fn(n)
					}
				} else if nodesAny, ok := chain["nodes"].([]any); ok {
					for _, nv := range nodesAny {
						if n, ok := nv.(map[string]any); ok {
							fn(n)
						}
					}
				}
			}
		}
	}
}

// FilterBandsBySpecificity scores, filters, and ranks candidate recall bands using
// deterministic feature hashing (memcore.FeatureHashVec), Shannon self-information
// coverage conservation, and spatial locus isolation (memcore.HierarchicalLCPScore).
func FilterBandsBySpecificity(rawPrompt, effectiveCue, workspace string, bands []map[string]any) []map[string]any {
	cleanPrompt := stripWorkspaceTokens(rawPrompt, workspace)
	promptBits := EffectiveShannonBits(cleanPrompt)
	if promptBits < 0.70*memcore.AtomicConceptEntropyBits || len(bands) == 0 {
		return nil
	}
	qVec := memcore.FeatureHashVec(cleanPrompt, memcore.DefaultHashDim)
	if len(qVec) == 0 {
		return nil
	}
	qFeats := extractLexicalFeatures(cleanPrompt)
	qSet := make(map[string]bool, len(qFeats))
	for _, f := range qFeats {
		qSet[f] = true
	}
	carryOverPaths := ExtractStructuralPaths(effectiveCue, 4)

	collectActiveNodes := func(b map[string]any) []map[string]any {
		var out []map[string]any
		iterBandNodes(b, func(n map[string]any) {
			if st := asString(n["status"]); st == "" || st == "None" || st == "active" {
				out = append(out, n)
			}
		})
		return out
	}

	df := make(map[string]int, 64)
	activeBandCount := 0
	for _, b := range bands {
		activeNodes := collectActiveNodes(b)
		if len(activeNodes) == 0 {
			continue
		}
		activeBandCount++
		title := StripTitleDateAndAuthorSuffix(strings.TrimPrefix(asString(b["title"]), "带·"))
		tFeats := extractLexicalFeatures(stripWorkspaceTokens(title, workspace))
		for _, f := range tFeats {
			df[f]++
		}
	}

	if activeBandCount == 0 {
		return nil
	}

	type scoredBand struct {
		band              map[string]any
		score             float64
		independentAnchor bool
	}
	var matched []scoredBand
	maxBandScore := 0.0
	maxRawScore := 0.0

	for _, b := range bands {
		activeNodes := collectActiveNodes(b)
		if len(activeNodes) == 0 {
			continue
		}
		if asString(b["kind"]) == "misc" || strings.HasPrefix(asString(b["title"]), "零散") {
			b = filterMiscBandBySpecificity(qVec, qSet, effectiveCue, workspace, b)
			if b == nil {
				continue
			}
			activeNodes = collectActiveNodes(b)
		}

		title := StripTitleDateAndAuthorSuffix(strings.TrimPrefix(asString(b["title"]), "带·"))
		effWS := workspace
		bRepo := extractBandRepo(b, workspace)
		lowerRawPrompt := strings.ToLower(rawPrompt)
		// Only strip a cross-repo name `bRepo` when the prompt itself mentions `bRepo`;
		// never strip an unmentioned cross-repo qualifier from the candidate title.
		if bRepo != "" && !strings.EqualFold(bRepo, workspace) && strings.Contains(lowerRawPrompt, strings.ToLower(bRepo)) {
			effWS = strings.TrimSpace(workspace + " " + bRepo)
		}
		cleanTitle := stripWorkspaceTokens(title, effWS)
		if memcore.StringShannonBits(cleanTitle) < 0.25*memcore.AtomicConceptEntropyBits {
			continue
		}
		coreTitle := stripWorkspaceTokens(StripTitleParenthetical(title), effWS)
		qCleanForBand := cleanPrompt
		if effWS != workspace {
			qCleanForBand = stripWorkspaceTokens(rawPrompt, effWS)
		}
		qSetForBand := qSet
		if qCleanForBand != cleanPrompt {
			qSetForBand = make(map[string]bool)
			for _, f := range extractLexicalFeatures(qCleanForBand) {
				qSetForBand[f] = true
			}
		}

		// 1. Spatial Locus Isolation (memcore.HierarchicalLCPScore)
		inActiveLocus := bandMatchesLocus(rawPrompt, workspace, b)

		// 2. Compute feature specificity coverage & dual-script coherence on Title and CoreTitle
		evalTitleFeats := func(target string) (rec float64, tri, bi, asc, unasc, minTDF, minMDF, rareCnt int, hasNonASCII, hitNonASCII, idHit bool) {
			tFeats := extractLexicalFeatures(target)
			if len(tFeats) == 0 {
				return 0, 0, 0, 0, 0, 999999, 999999, 0, false, false, false
			}
			minTDF = 999999
			minMDF = 999999
			var hitW, totW float64
			for _, f := range tFeats {
				dInt := df[f]
				if onlineDF := globalCorpusDFValue(f); onlineDF > dInt {
					dInt = onlineDF
				}
				if dInt <= 0 {
					dInt = 1
				}
				if !strings.HasPrefix(f, "c1:") && dInt < minTDF {
					minTDF = dInt
				}
				d := float64(dInt)
				w := 1.0 / (1.0 + math.Log(1.0+d))
				if dInt == 1 {
					w *= 1.65
				} else if dInt == 2 {
					w *= 1.25
				}
				if strings.HasPrefix(f, "id:") {
					w *= 2.0
				} else if strings.HasPrefix(f, "t:") {
					w *= 1.35
				} else if strings.HasPrefix(f, "w:") {
					w *= 1.30
				}
				if !strings.HasPrefix(f, "t:") {
					totW += w
				}
				isHit := qSetForBand[f]
				if isHit && !strings.HasPrefix(f, "c1:") && !strings.HasPrefix(f, "t:") {
					if dInt < minMDF {
						minMDF = dInt
					}
					if dInt <= 3 {
						rareCnt++
					}
				}
				if strings.HasPrefix(f, "id:") || strings.HasPrefix(f, "num:") || strings.HasPrefix(f, "w2:") || strings.HasPrefix(f, "w3:") || strings.HasPrefix(f, "w:") {
					if isHit {
						hitW += w
						asc++
						if strings.HasPrefix(f, "id:") {
							idHit = true
						}
					} else {
						unasc++
					}
				} else {
					hasNonASCII = true
					if isHit && !strings.HasPrefix(f, "c1:") {
						if strings.HasPrefix(f, "t:") {
							hitW += w * 0.55
							tri++
						} else {
							hitW += w
							bi++
						}
						hitNonASCII = true
					}
				}
			}
			if totW > 0 {
				rec = hitW / totW
			}
			return
		}

		evalTarget := coreTitle
		if memcore.StringShannonBits(evalTarget) < 0.70*memcore.AtomicConceptEntropyBits {
			evalTarget = cleanTitle
		}
		specRecall, matchedTri, matchedBi, matchedASCII, unmatchedASCII, _, minMatchedDF, rareMatches, hasNonASCII, hitNonASCII, titleIDHit := evalTitleFeats(evalTarget)
		covBits, totBits, spanCount, bitCov, ideoCovBits, ideoTotBits, ideoSpans, ideoBitCov := computeBipartiteMaskTopology(qSetForBand, qCleanForBand, evalTarget)
		if evalTarget != cleanTitle {
			recFull, triFull, biFull, ascFull, unascFull, _, _, rareFull, hasNFull, hitNFull, idFull := evalTitleFeats(cleanTitle)
			if ascFull > matchedASCII || idFull {
				covF, totF, spanF, bitF, iCovF, iTotF, iSpanF, iBitF := computeBipartiteMaskTopology(qSetForBand, qCleanForBand, cleanTitle)
				if covF > covBits || spanF > spanCount || idFull {
					specRecall, matchedTri, matchedBi, matchedASCII, unmatchedASCII, rareMatches, hasNonASCII, hitNonASCII, titleIDHit = recFull, triFull, biFull, ascFull, unascFull, rareFull, hasNFull, hitNFull, idFull
					covBits, totBits, spanCount, bitCov, ideoCovBits, ideoTotBits, ideoSpans, ideoBitCov = covF, totF, spanF, bitF, iCovF, iTotF, iSpanF, iBitF
				}
			}
		}
		if (spanCount >= 2 && covBits >= 24.0 && (ideoTotBits < 40.0 || ideoSpans >= 2 || ideoCovBits >= 36.0)) || covBits >= 46.0 || (rareMatches >= 1 && covBits >= 24.0) {
			if bm25 := specRecall * 1.45; bm25 > specRecall {
				specRecall = math.Min(1.0, bm25)
			}
		}

		// 3. Evaluate FeatureHashVec Cosine Similarity & Body Lexical Coverage
		tVec := memcore.FeatureHashVec(cleanTitle, memcore.DefaultHashDim)
		var bandBodyBuilder strings.Builder
		for _, n := range activeNodes {
			bandBodyBuilder.WriteString(" ")
			bandBodyBuilder.WriteString(asString(n["label"]))
			bandBodyBuilder.WriteString(" ")
			bandBodyBuilder.WriteString(asString(n["content"]))
		}
		geoTitleSim := memcore.DotUnit(qVec, tVec)

		bodyClean := stripWorkspaceTokens(bandBodyBuilder.String(), effWS)
		bodyFeats := extractLexicalFeatures(bodyClean)
		bodyMatchedBi := 0
		bodyMatchedTri := 0
		bodyIDHit := false
		for _, bf := range bodyFeats {
			if qSetForBand[bf] {
				if strings.HasPrefix(bf, "id:") {
					bodyIDHit = true
				} else if strings.HasPrefix(bf, "t:") {
					bodyMatchedTri++
				} else {
					bodyMatchedBi++
				}
			}
		}

		// 4. Structural Path Coordinate Check
		coordMatched := bandMatchesPaths(b, rawPrompt, nil)
		if !coordMatched && promptBits <= 68.0 && len(carryOverPaths) > 0 && unmatchedASCII == 0 &&
			(bitCov >= 0.20 || bodyMatchedTri >= 1 || bodyMatchedBi >= 3 || titleIDHit || bodyIDHit) {
			coordMatched = bandMatchesPaths(b, effectiveCue, carryOverPaths)
		}

		// 5. Dual-Script Orthogonality & Cross-Locus Gate (measured strictly in Shannon bits)
		if !coordMatched {
			if unmatchedASCII > 0 && hasNonASCII && (!hitNonASCII || (matchedTri == 0 && matchedBi <= 1 && !titleIDHit && !bodyIDHit)) {
				continue
			}
			if matchedASCII == 0 && unmatchedASCII > 0 && specRecall < 0.48 && !bodyIDHit {
				continue
			}
			if !hasNonASCII && unmatchedASCII > 0 && specRecall < 0.72 {
				continue
			}
			if hasNonASCII && !hitNonASCII && matchedASCII > 0 && !titleIDHit && !bodyIDHit {
				if unmatchedASCII > 0 || (ideoTotBits >= 20.0 && minMatchedDF > 4 && bodyMatchedBi < 2) {
					continue
				}
			}
			if totBits <= 10.0 && promptBits >= 64.0 && !titleIDHit && !bodyIDHit {
				continue
			}
			if ideoTotBits >= 50.0 && ideoSpans <= 1 && !(matchedASCII >= 1 && unmatchedASCII == 0 && minMatchedDF <= 3 && ideoBitCov >= 0.40) && ideoBitCov < 0.45 && bitCov < 0.58 && !titleIDHit && !bodyIDHit && !(inActiveLocus && ideoCovBits >= 26.0 && (bodyMatchedTri >= 2 || bodyMatchedBi >= 5)) {
				continue
			}
			if promptBits >= 200.0 && ideoTotBits >= 40.0 && ideoCovBits <= 20.0 && matchedTri == 0 && rareMatches == 0 && ideoSpans <= 1 && !titleIDHit && !bodyIDHit && bodyMatchedTri == 0 && bodyMatchedBi < 4 {
				continue
			}
			if promptBits >= 150.0 && spanCount <= 1 && covBits <= 30.0 && totBits >= 46.0 && (rareMatches == 0 || promptBits >= 240.0) && !titleIDHit && !bodyIDHit && bodyMatchedTri == 0 && bodyMatchedBi < 4 {
				continue
			}
			if !inActiveLocus && !titleIDHit && !bodyIDHit && (specRecall < 0.36 || (spanCount <= 1 && bitCov < 0.45)) {
				continue
			}
			if matchedTri == 0 && rareMatches == 0 && matchedASCII == 0 && !titleIDHit && !bodyIDHit && totBits >= 46.0 &&
				!(promptBits < 110.0 && bitCov >= 0.65) && !(inActiveLocus && covBits >= 36.0 && bodyMatchedTri >= 2) {
				continue
			}
			if spanCount <= 1 && totBits >= 56.0 && bitCov < 0.45 && !titleIDHit && !bodyIDHit && !(inActiveLocus && covBits >= 26.0 && (bodyMatchedTri >= 2 || bodyMatchedBi >= 5)) {
				continue
			}
			if promptBits >= 150.0 && spanCount <= 1 && covBits <= 40.0 && !titleIDHit && !bodyIDHit && bitCov < 0.55 && bodyMatchedTri == 0 && bodyMatchedBi < 5 {
				continue
			}
			if matchedTri == 0 && matchedBi <= 1 && matchedASCII == 0 && !titleIDHit && !bodyIDHit && bitCov < 0.50 {
				continue
			}
		}

		passes := coordMatched ||
			(spanCount >= 2 && covBits >= 24.0 && (matchedTri >= 1 || rareMatches >= 1 || matchedASCII >= 1 || bodyMatchedTri >= 2) && (bitCov >= 0.22 || specRecall >= 0.26)) ||
			(bitCov >= 0.45 && specRecall >= 0.32 && (matchedTri >= 1 || (matchedASCII >= 1 && unmatchedASCII == 0))) ||
			(matchedASCII >= 1 && unmatchedASCII == 0 && hitNonASCII && (spanCount >= 2 || bitCov >= 0.36 || (matchedASCII >= 2 && matchedBi >= 1))) ||
			(matchedASCII >= 1 && unmatchedASCII == 0 && !hasNonASCII && bitCov >= 0.50) ||
			((titleIDHit || bodyIDHit) && (unmatchedASCII == 0 || (matchedBi >= 1 && bodyMatchedBi >= 2)) && (matchedBi >= 1 || bodyMatchedBi >= 2 || bitCov >= 0.25)) ||
			(inActiveLocus && covBits >= 26.0 && bitCov >= 0.25 && (bodyMatchedTri >= 2 || bodyMatchedBi >= 5) && unmatchedASCII == 0)

		if !passes {
			continue
		}

		specScore := specRecall*3.5 + bitCov*2.5 + geoTitleSim*2.5 + float64(rareMatches)*1.8 + float64(spanCount)*0.9 + (covBits/memcore.AtomicConceptEntropyBits)*0.35 + float64(matchedASCII)*0.85 + float64(bodyMatchedTri)*0.35 + float64(bodyMatchedBi)*0.12
		finalScore := specScore
		if coordMatched && finalScore < 4.8 {
			finalScore = 4.8
		}
		if finalScore > maxBandScore {
			maxBandScore = finalScore
		}
		if rsc := peakBandRawScore(b); rsc > maxRawScore {
			maxRawScore = rsc
		}
		indep := coordMatched || titleIDHit || bodyIDHit || (specRecall >= 0.36 && (matchedTri >= 1 || matchedBi >= 2 || matchedASCII >= 1))
		matched = append(matched, scoredBand{
			band:              b,
			score:             finalScore,
			independentAnchor: indep,
		})
	}

	if len(matched) == 0 {
		return nil
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].score > matched[j].score
	})

	var kept []map[string]any
	for i, sb := range matched {
		if !sb.independentAnchor && maxBandScore >= 3.5 && sb.score < maxBandScore*0.58 {
			continue
		}
		if i == 0 || sb.independentAnchor || maxRawScore <= 0 || peakBandRawScore(sb.band) >= maxRawScore*0.55 {
			kept = append(kept, sb.band)
		}
	}
	return kept
}

func filterMiscBandBySpecificity(qVec []float32, qSet map[string]bool, effectiveCue, workspace string, band map[string]any) map[string]any {
	nodes, ok := band["nodes"].([]map[string]any)
	if !ok || len(nodes) == 0 {
		return nil
	}
	lowerWS := strings.ToLower(workspace)
	lowerCue := strings.ToLower(effectiveCue)
	var kept []map[string]any
	for _, n := range nodes {
		if st := asString(n["status"]); st != "" && st != "None" && st != "active" {
			continue
		}
		loc := strings.ToLower(strings.TrimSpace(asString(n["locus"])))
		if loc != "" && loc != "none" && lowerWS != "" {
			parts := strings.Split(loc, "/")
			repo := parts[0]
			if len(parts) >= 2 {
				repo = parts[1]
			}
			if repo != "" && len(repo) >= 4 && repo != lowerWS && !strings.Contains(lowerCue, repo) {
				continue
			}
		}
		text := stripWorkspaceTokens(asString(n["label"])+" "+asString(n["content"]), workspace)
		nFeats := extractLexicalFeatures(text)
		hits := 0
		triHits := 0
		for _, nf := range nFeats {
			if qSet[nf] {
				hits++
				if strings.HasPrefix(nf, "t:") || strings.HasPrefix(nf, "id:") {
					triHits++
				}
			}
		}
		if triHits >= 1 && hits >= 2 || hits >= 4 {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(band))
	for k, v := range band {
		cloned[k] = v
	}
	cloned["nodes"] = kept
	cloned["chains"] = []any{}
	return cloned
}

func bandMatchesPaths(band map[string]any, cue string, carryOverPaths []string) bool {
	paths := carryOverPaths
	if len(paths) == 0 {
		paths = ExtractStructuralPaths(cue, 4)
	}
	if len(paths) == 0 {
		return false
	}
	var corpus strings.Builder
	corpus.WriteString(strings.ToLower(asString(band["title"])))
	iterBandNodes(band, func(n map[string]any) {
		for _, k := range []string{"label", "content"} {
			if v := asString(n[k]); v != "" && v != "None" {
				corpus.WriteString(" ")
				corpus.WriteString(strings.ToLower(v))
			}
		}
	})
	lowerCorpus := corpus.String()
	for _, p := range paths {
		lp := strings.ToLower(strings.TrimSpace(p))
		if len(carryOverPaths) > 0 && (strings.HasSuffix(lp, ".db") || strings.HasSuffix(lp, ".sqlite") || strings.HasSuffix(lp, ".log")) {
			continue
		}
		if len(lp) >= 6 && strings.Contains(lowerCorpus, lp) {
			return true
		}
		if base := filepath.Base(lp); len(base) >= 8 && strings.Contains(lp, "/") && strings.Contains(lowerCorpus, base) {
			return true
		}
	}
	return false
}

func extractBandRepo(band map[string]any, activeWorkspace ...string) string {
	proj := ""
	if len(activeWorkspace) > 0 {
		proj = strings.ToLower(strings.TrimSpace(activeWorkspace[0]))
	}
	extractRepo := func(loc string) string {
		loc = strings.ToLower(strings.TrimSpace(loc))
		if loc == "" || loc == "none" || loc == "null" {
			return ""
		}
		parts := strings.Split(loc, "/")
		if len(parts) >= 2 {
			return parts[1]
		}
		return parts[0]
	}
	foundLocusRepo := ""
	otherRepo := ""
	iterBandNodes(band, func(n map[string]any) {
		if otherRepo != "" {
			return
		}
		if r := extractRepo(asString(n["locus"])); r != "" && len(r) >= 4 {
			if r != proj {
				otherRepo = r
				return
			}
			foundLocusRepo = r
		}
	})
	if otherRepo != "" {
		return otherRepo
	}
	if proj != "" {
		if dash := strings.IndexByte(proj, '-'); dash >= 3 {
			prefix := proj[:dash+1]
			for _, tok := range strings.FieldsFunc(strings.ToLower(asString(band["title"])), func(r rune) bool {
				return !(unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-')
			}) {
				if strings.HasPrefix(tok, prefix) && len(tok) > len(prefix)+1 && tok != proj {
					return tok
				}
			}
		}
	}
	return foundLocusRepo
}

func bandMatchesLocus(prompt, workspace string, band map[string]any) bool {
	if workspace == "" {
		return true
	}
	lowerProj := strings.ToLower(workspace)
	lowerPrompt := strings.ToLower(prompt)
	title := strings.ToLower(asString(band["title"]))
	bandRepo := extractBandRepo(band, workspace)
	if bandRepo != "" && bandRepo != lowerProj {
		lcp := memcore.HierarchicalLCPScore("rocareer/"+lowerProj, "rocareer/"+bandRepo)
		if lcp < 0.99 && !strings.Contains(lowerPrompt, bandRepo) && !strings.Contains(title, lowerProj) {
			return false
		}
	}
	return true
}

// PeakBandScore returns the peak recall score across a band and its nodes/chains.
func PeakBandScore(band map[string]any) float64 {
	return peakBandRawScore(band)
}

func peakBandRawScore(band map[string]any) float64 {
	peak := 0.0
	if s, ok := band["score"].(float64); ok && s > peak {
		peak = s
	}
	iterBandNodes(band, func(n map[string]any) {
		if s, ok := n["score"].(float64); ok && s > peak {
			peak = s
		}
	})
	return peak
}

// DetectTopGateFilteredBand returns the highest-similarity candidate band title that had
// partial lexical signal before being filtered by the specificity gate.
func DetectTopGateFilteredBand(rawPrompt, workspace string, bands []map[string]any) string {
	cleanPrompt := stripWorkspaceTokens(rawPrompt, workspace)
	if EffectiveShannonBits(cleanPrompt) < 0.70*memcore.AtomicConceptEntropyBits || len(bands) == 0 {
		return ""
	}
	qFeats := extractLexicalFeatures(cleanPrompt)
	qSet := make(map[string]bool, len(qFeats))
	for _, f := range qFeats {
		qSet[f] = true
	}
	bestTitle := ""
	bestScore := 0.0
	for _, b := range bands {
		title := strings.TrimSpace(asString(b["title"]))
		clean := stripWorkspaceTokens(StripTitleDateAndAuthorSuffix(strings.TrimPrefix(title, "带·")), workspace)
		tFeats := extractLexicalFeatures(clean)
		if len(tFeats) == 0 {
			continue
		}
		hits := 0
		for _, f := range tFeats {
			if qSet[f] {
				hits++
			}
		}
		cov := float64(hits) / float64(len(tFeats))
		if hits >= 2 && cov >= 0.25 {
			sc := float64(hits) + cov
			if sc > bestScore {
				bestScore = sc
				bestTitle = title
			}
		}
	}
	return bestTitle
}

// SetCorpusTokenDFForRecall 注入当前分区/金库的在线文档频次表 (df) 与总文档数 (totalDocs)，
// 供 EndogenousDFConjugateBits 与 BPEShannonCompressionPurity 执行零高级算子的在线 DF 贝叶斯共轭洗脱。
func SetCorpusTokenDFForRecall(df map[string]int, totalDocs int) {
	publishGlobalCorpusDF(df, totalDocs)
}

// EndogenousDFConjugateBits 计算词元 w 在在线文档频次 (df, totalDocs) 下的贝叶斯冷热共轭有效自信息（量纲 Bits）：
// 直接委托给 memcore.OnlineDFConjugateBits（双仓唯一实现真源，零高级图算子开销）。
func EndogenousDFConjugateBits(w string, bChar, coldBits float64, df, totalDocs int) float64 {
	_ = w
	return memcore.OnlineDFConjugateBits(bChar, coldBits, df, totalDocs)
}

// BPEShannonCompressionPurity computes the zero-wordlist lexical content purity ratio rho(w) in [0.12, 1.0]
// by coupling cold-start lexical self-information with online DF Bayesian conjugate washout (EndogenousDFConjugateBits).
func BPEShannonCompressionPurity(tk string) float64 {
	w := strings.ToLower(strings.TrimSpace(memcore.CanonicalizeApostrophe(tk)))
	if w == "" {
		return 0
	}
	bChar := memcore.StringShannonBits(w)
	if bChar < 4.0 {
		return 0.12
	}

	var df, totalDocs int
	if onlineDF, onlineTotalDocs, installed := globalCorpusDFProbe(w); installed {
		df = onlineDF
		totalDocs = onlineTotalDocs
	}

	// 仅在尚未被在线高频刷屏证伪（df < 2 或 df/N <= 1/e）时采信静态专名/动词根先验；
	// 一旦在线 DF 证明某词是高频刷屏套话（df >= 2 且 df/N > 1/e），跳过静态免死金牌，交由在线 DF 共轭洗脱！
	isOnlineBoilerplate := totalDocs >= 4 && df >= 2 && float64(df)/float64(totalDocs) > (1.0/math.E)
	if !isOnlineBoilerplate {
		if tokenzr.IsBPEProperNounPrior(w) || IsExtendedInflectedVerbRoot(w) {
			return 1.0
		}
	}

	coldEff := tokenzr.EffectiveLexicalBits(w)
	eff := EndogenousDFConjugateBits(w, bChar, coldEff, df, totalDocs)

	if memcore.IsPureCJK(w) {
		deficitRatio := eff / bChar
		if deficitRatio >= 0.72 {
			return 1.0
		}
		rho := 0.12 + 0.88*((deficitRatio-0.52)/0.20)
		return math.Max(0.12, math.Min(1.0, rho))
	}
	if eff >= 12.0 {
		return 1.0
	}
	rho := 0.12 + 0.88*((eff-9.5)/2.5)
	return math.Max(0.12, math.Min(1.0, rho))
}

// IsExtendedInflectedVerbRoot reports whether word is an open-class root in the 128k BPE merge tree
// using pure BPE subword valence and Shannon stem neighborhoods (zero language-specific suffix strings).
func IsExtendedInflectedVerbRoot(word string) bool {
	w := strings.ToLower(strings.TrimSpace(word))
	if tokenzr.BPEInflectionValence(w) >= 2 {
		return true
	}
	if memcore.StringShannonBits(w) < memcore.AtomicConceptEntropyBits {
		return false
	}
	_, _, cnt4k := tokenzr.BPEPrefixStats(w)
	return tokenzr.BPEInflectionValence(w) >= 1 && cnt4k == 0 && tokenzr.EffectiveLexicalBits(w) >= 11.2
}

// IsExtendedInflectedVerbToken determines whether word is an already-inflected finite verb form
// ("moved", "lives", "studies", "watches", "rusted") using pure BPE merge-tree terminal state
// (tokenzr.IsAlreadyInflectedVerbToken) and Shannon Bit-Field morphological root alignment (zero suffix strings).
func IsExtendedInflectedVerbToken(word string) bool {
	return tokenzr.IsAlreadyInflectedVerbToken(word)
}

// isBPEPolysemousHomograph identifies closed-class tokens that simultaneously possess open-class nominal/proper
// identity in the 128k BPE merge tree (zero wordlists: requires StringShannonBits >= 6.0b, single-token TitleCase
// representation in BPE, and either non-zero BPEInflectionValence or compact TitleCase-to-lowercase BPE prior gap).
func isBPEPolysemousHomograph(w string) bool {
	if memcore.StringShannonBits(w) < 6.0 || memcore.IsPureCJK(w) {
		return false
	}
	if tokenzr.BPEInflectionValence(w) >= 1 {
		return true
	}
	rs := []rune(w)
	if len(rs) == 0 || !unicode.IsLetter(rs[0]) {
		return false
	}
	rs[0] = unicode.ToUpper(rs[0])
	title := string(rs)
	if tokenzr.Count(" "+title) != 1 {
		return false
	}
	gap := tokenzr.TokenPriorBits(title) - tokenzr.TokenPriorBits(w)
	return gap <= 2.2 && tokenzr.TokenPriorBits(w) >= 7.8
}

// ComputeContextualTokenWeights implements the 3-layer 100% zero-wordlist disambiguation and continuous weighting architecture:
//  1. Layer 1 (Continuous Soft Non-Zero Floor): Never hard-drops any lexical token with StringShannonBits >= 6.0b;
//     suspected function words receive a bounded soft floor in [0.12, 0.35] proportional to BPEShannonCompressionPurity(w);
//  2. Layer 2 (Local BPE Inflectional & Syntactic Contour, 0 hardcoded words): Polysemous BPE homographs
//     (isBPEPolysemousHomograph) preceding finite inflected verbs (IsExtendedInflectedVerbToken), at clause-final
//     non-auxiliary object position (i == m-1), or in auxiliary-inverted subject position receive full weight 1.0;
//  3. Layer 3 (structured memory assertion Graph Ontology Grounding): Any token matching a materialized entity node in knownEntityLabels
//     is immediately promoted to 1.0.
func ComputeContextualTokenWeights(q string, knownEntityLabels map[string]bool) map[string]float64 {
	q = memcore.CanonicalizeApostrophe(q)
	if strings.TrimSpace(q) == "" {
		return nil
	}
	weights := make(map[string]float64, 16)
	onlineDFTable, onlineTotalDocs, onlineInstalled := globalCorpusDFSnapshot()
	totalDocs := onlineTotalDocs
	if totalDocs <= 0 && onlineInstalled {
		totalDocs = len(onlineDFTable)
	}
	for _, tk := range memcore.Tokenize(q) {
		if onlineInstalled && totalDocs >= 4 {
			if df := onlineDFTable[tk]; df >= 2 && float64(df)/float64(totalDocs) > (1.0/math.E) {
				continue
			}
		}
		// 只把高增益词元与 CJK 词元预置为满权；低增益拉丁胶水词元留给 Layer 1 软地板，
		// 否则会绕过 Layer 2 的句法轮廓判定（零词表，纯信息增益分层）。
		if memcore.IsHighGainToken(tk) || memcore.IsPureCJK(tk) {
			weights[tk] = 1.0
		}
	}
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	m := len(words)
	for i, w := range words {
		if w == "" {
			continue
		}
		// Layer 3: structured memory assertion Graph Entity Grounding
		if knownEntityLabels != nil && knownEntityLabels[w] {
			weights[w] = 1.0
			continue
		}
		// Layer 2: 100% Zero-Wordlist BPE Inflectional & Positional Contour
		if isBPEPolysemousHomograph(w) {
			// (a) Pre-inflected-verb subject slot: modals cannot precede an already-inflected finite verb
			if i+1 < m && IsExtendedInflectedVerbToken(words[i+1]) {
				weights[w] = 1.0
				continue
			}
			// (b) Clause-final object slot: modal auxiliaries can never terminate a multi-word clause
			if i >= 1 && i == m-1 {
				weights[w] = 1.0
				continue
			}
			// (c) Inverted question subject slot: [interrogative, aux, subject_homograph, open_class_verb...]
			if i >= 2 && i+1 < m && weights[words[i-1]] < 0.85 && tokenzr.BPEInflectionValence(words[i+1]) >= 2 {
				weights[w] = 1.0
				continue
			}
			// (d) Calendar slot: a polysemous homograph directly followed by a year numeral
			// is a calendar month, never a modal/auxiliary (pure numeric contour, zero word lists).
			if i+1 < m {
				if y, err := strconv.Atoi(words[i+1]); err == nil && y >= 1000 && y <= 3000 {
					weights[w] = 1.0
					continue
				}
			}
		}
		if weights[w] >= 1.0 {
			continue
		}
		// Layer 1: Continuous Soft Floor & Online DF Rarity Promotion
		if onlineInstalled && totalDocs >= 4 {
			if df := onlineDFTable[w]; df >= 1 && float64(df)/float64(totalDocs) <= (1.0/(math.E*math.E)) {
				if purity := BPEShannonCompressionPurity(w); purity >= 0.85 {
					weights[w] = purity
					continue
				}
			}
		}
		if !memcore.IsPureCJK(w) && memcore.StringShannonBits(w) >= 6.0 {
			soft := 0.30 * BPEShannonCompressionPurity(w)
			if soft < 0.12 {
				soft = 0.12
			}
			weights[w] = soft
		}
	}
	return weights
}
