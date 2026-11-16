// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/text"
)

var apostropheReplacer = strings.NewReplacer("’", "'", "‘", "'", "`", "'")

// CanonicalizeApostrophe normalizes curly apostrophes and backticks into ASCII single quote (delegates to internal/text).
func CanonicalizeApostrophe(s string) string { return text.CanonicalizeApostrophe(s) }

// NormalizeGap normalizes an unbound slot reason string.
func NormalizeGap(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// IsRolePlaceholder reports whether s is a generic dialog transcript role placeholder (delegates to internal/text).
func IsRolePlaceholder(s string) bool { return text.IsRolePlaceholder(s) }

// ResolveRoleSpeaker maps generic transcript role tokens ("user", "assistant") to registered
// speaker identities when configured; returns raw unchanged otherwise.
func ResolveRoleSpeaker(raw, userIdentity, assistantIdentity string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "user", "human":
		if u := strings.TrimSpace(userIdentity); u != "" {
			return u
		}
	case "assistant", "ai", "bot":
		if a := strings.TrimSpace(assistantIdentity); a != "" {
			return a
		}
	}
	return raw
}

// ExtractPossessiveOwner extracts the leading named owner from a possessive noun phrase
// ("Alice's dog" -> "Alice", "张三的豆豆" -> "张三"). Returns "" if not a qualified possessive,
// if the prefix is a pronoun/placeholder, or if the prefix in a cased script is a lowercase common noun ("children's hospital").
//
// 代码放宽零误杀 & 香农只负责增益（架构契约）：
// 移除 IsHighGainToken(strings.ToLower(owner)) 硬截断阈值（实测会误杀 "Jo's dog" / "Bo's cat" / "Al's car" 等 2 字母名与常见单字名）。
func ExtractPossessiveOwner(label string) string {
	s := strings.TrimSpace(CanonicalizeApostrophe(label))
	if s == "" {
		return ""
	}
	isValidOwner := func(owner string) bool {
		if owner == "" || IsRolePlaceholder(owner) || CountTokens(owner) > TokenTier4 {
			return false
		}
		bits := StringShannonBits(owner)
		if bits == 0 || (bits == 2.0 && !hasProperCapitalInSpan(owner)) {
			return false
		}
		if hasCasedLetter(owner) && !hasProperCapitalInSpan(owner) {
			return false
		}
		// 2 字母首字母大写词接属格（如 "Al's", "Jo's", "Bo's"）：除缩略词 "It" 外均为合法 2 字母短人名宿主
		if bits == 4.0 && hasCasedLetter(owner) && hasProperCapitalInSpan(owner) {
			return !strings.EqualFold(owner, "it")
		}
		return !IsPronounSurface(owner)
	}
	lo := strings.ToLower(s)
	if idx := strings.Index(lo, "'s "); idx > 0 {
		owner := strings.TrimSpace(s[:idx])
		if isValidOwner(owner) {
			return owner
		}
	}
	for _, marker := range []string{"的", "の"} {
		if idx := strings.Index(s, marker); idx > 0 {
			owner := strings.TrimSpace(s[:idx])
			rest := strings.TrimSpace(s[idx+len(marker):])
			if rest != "" && isValidOwner(owner) {
				return owner
			}
		}
	}
	return ""
}

// FormatOwnerQualifiedLabel formats an owner-qualified entity label ("<owner>的<label>" for CJK scripts,
// "<owner>'s <label>" for alphabetic scripts) when disambiguating homonym entities owned by different subjects.
func FormatOwnerQualifiedLabel(owner, label string) string {
	owner = strings.TrimSpace(CanonicalizeApostrophe(owner))
	label = strings.TrimSpace(CanonicalizeApostrophe(label))
	if owner == "" || label == "" || strings.EqualFold(owner, label) {
		return label
	}
	if existingOwner := ExtractPossessiveOwner(label); existingOwner != "" {
		return label
	}
	loOwner := strings.ToLower(owner)
	loLabel := strings.ToLower(label)
	if strings.HasPrefix(loLabel, loOwner+"'s ") || strings.HasPrefix(loLabel, loOwner+"的") {
		return label
	}
	for _, r := range owner + label {
		if IsContinuousSyllabicOrIdeographic(r) {
			return owner + "的" + label
		}
	}
	return owner + "'s " + label
}

// IsReduplicatedCJK reports whether s is a pure-CJK reduplicated surface (AA, e.g. "妈妈", "豆豆").
func IsReduplicatedCJK(s string) bool {
	s = strings.TrimSpace(s)
	if !IsPureCJK(s) {
		return false
	}
	r0, w0 := utf8.DecodeRuneInString(s)
	if w0 <= 0 || w0 >= len(s) {
		return false
	}
	r1, w1 := utf8.DecodeRuneInString(s[w0:])
	return w0+w1 == len(s) && r0 == r1
}

// TrimTrailingGeminateRune strips a trailing doubled character (e.g. "mell" -> "mel").
// 纯形式层叠字剥离（架构契约「香农只做增益不做判官」）：叠字形式本身即足以支撑该机械动作，
// 不再以 `StringShannonBits(cand) >= 6.0` 充当「剥完还够不够格」的生死判据（放宽、零误杀）。
func TrimTrailingGeminateRune(s string) string {
	rLast, wLast := utf8.DecodeLastRuneInString(s)
	if wLast > 0 && len(s) > wLast {
		cand := s[:len(s)-wLast]
		rPrev, _ := utf8.DecodeLastRuneInString(cand)
		if rLast == rPrev {
			return cand
		}
	}
	return s
}

func sharesAnyRune(a, b string) bool {
	set := make(map[rune]bool, len(a))
	for _, r := range a {
		if !unicode.IsSpace(r) {
			set[unicode.ToLower(r)] = true
		}
	}
	for _, r := range b {
		if set[unicode.ToLower(r)] {
			return true
		}
	}
	return false
}

// AliasSpec is one alias binding request on remember.
type AliasSpec struct {
	Of    string `json:"of"`
	Alias string `json:"alias"`
	Basis string `json:"basis"`
}

// AliasBasisSet is the closed set of valid alias basis declarations.
var AliasBasisSet = aliasBasisClosedSet

var aliasBasisClosedSet = map[string]bool{
	"self":       true,
	"referral":   true,
	"relation":   true,
	"explicit":   true,
	"shortening": true,
}

func hasProperCapitalWord(words []string) bool {
	for _, w := range words {
		w = strings.Trim(w, "\"'“”‘’.,!?;:()[]{}")
		if r, sz := utf8.DecodeRuneInString(w); sz > 0 && unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func hasProperCapitalInSpan(span string) bool {
	inWord := false
	for _, r := range span {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			if strings.ContainsRune("\"'“”‘’.,!?;:()[]{}", r) {
				continue
			}
			inWord = true
			if unicode.IsUpper(r) {
				return true
			}
		}
	}
	return false
}

func hasCasedLetter(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) || unicode.IsLower(r) {
			return true
		}
	}
	return false
}

func hasPossessiveMarker(lo string) bool {
	return strings.Contains(lo, "'s ") || strings.HasSuffix(lo, "'s") ||
		strings.Contains(lo, "s' ") || strings.HasSuffix(lo, "s'")
}

// hasLetterOrDigit reports whether s carries at least one Unicode letter or digit.
// 结构判据（非熵阈值）：用于拒收纯标点/纯符号的非载体表面。
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// IsInvalidAliasSurface reports whether an alias surface violates the **mechanical** label contract.
//
// 判据边界（架构契约「代码里的语义词闸门全拆 · 宁宽勿误拒」）：本闸只回答「这个字符串
// 能不能当数据库实体标签」，一律不回答「这个词/短语是什么意思、够不够格当别名」——语义裁决 100% 归 LLM。
// 保留（机械契约，防**错**与防**丢**）：空表面 / 保留标识符（角色词与本体代号，防 Assistant 本体标识符冲突）/
// 非载体表面（零字母数字）/ 超列宽与子句标点。
// 已拆除（语义词闸门 —— 宁脏勿误拒、零丢弃）：代词表面 / 首人称领属 / 「X的Y」跨类属格与属主交叉 /
// 叠字称谓 / 同姓异名碰撞 / 大小写专名推定 / 上下位嵌入判据 / 香农先验阈值。
func IsInvalidAliasSurface(of, alias string) (bool, string) {
	_ = of // 保留签名：语义词闸门已全部拆除，宿主（of）不参与机械契约判定
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return true, "empty_alias"
	}
	loAlias := strings.ToLower(CanonicalizeApostrophe(alias))
	if IsRolePlaceholder(loAlias) || IsSelfDesignator(loAlias) {
		return true, "role_placeholder"
	}
	// 结构不变量（非载体表面）：表面不含任何字母/数字 ⇒ 不可能是实体名（零语种分支、零熵阈值）。
	if !hasLetterOrDigit(alias) {
		return true, "non_carrier_surface"
	}
	if len([]rune(alias)) > NodeLabelColumnCap || strings.ContainsAny(alias, "!?！？") || strings.HasSuffix(alias, "'") || strings.HasSuffix(alias, "’") {
		return true, "clause_or_overlong"
	}
	return false, ""
}

// IsSubjectObjectCrossSlotAlias detects when an inline alias attached to a binary assertion (S —[R]→ O)
// mistakenly binds a proper token from the opposite slot (e.g. S="Daisy", O="hexagonal white name tag reading BERNIE",
// with alias {of:"Daisy", alias:"BERNIE"}) where the alias has zero character/prefix overlap with `of`
// yet appears verbatim as a word inside the opposite slot.
func IsSubjectObjectCrossSlotAlias(subject, object, of, alias string) bool {
	s := strings.TrimSpace(subject)
	o := strings.TrimSpace(object)
	of = strings.TrimSpace(of)
	alias = strings.TrimSpace(alias)
	if s == "" || o == "" || of == "" || alias == "" || strings.EqualFold(s, o) || strings.EqualFold(of, alias) {
		return false
	}
	if IsPlainShortening(of, alias) || IsPlainShortening(alias, of) {
		return false
	}
	loOf := strings.ToLower(of)
	loAl := strings.ToLower(alias)
	if strings.Contains(loOf, loAl) || strings.Contains(loAl, loOf) {
		return false
	}
	var opp string
	if strings.EqualFold(of, s) {
		opp = strings.ToLower(o)
	} else if strings.EqualFold(of, o) {
		opp = strings.ToLower(s)
	} else {
		return false
	}
	if opp == loAl {
		return true
	}
	for _, w := range strings.FieldsFunc(opp, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		// 逐字整词相等即跨槽同指（纯形式判据）；不再以香农阈值补一刀资格门。
		if w == loAl {
			return true
		}
	}
	return false
}

// IsPlainShortening checks whether alias is a true character prefix or 2-char non-ASCII shortening of of.
func IsPlainShortening(of, alias string) bool {
	of, alias = strings.TrimSpace(of), strings.TrimSpace(alias)
	if of == "" || alias == "" || strings.EqualFold(of, alias) {
		return false
	}
	if hasCasedLetter(of) && hasCasedLetter(alias) &&
		hasProperCapitalInSpan(of) != hasProperCapitalInSpan(alias) {
		return false
	}
	lo := strings.ToLower(CanonicalizeApostrophe(of))
	la := strings.ToLower(CanonicalizeApostrophe(alias))
	if lo != la && strings.HasPrefix(lo, la) {
		rest := lo[len(la):]
		if strings.HasPrefix(rest, "'") {
			return false
		}
		return true
	}
	bLa := StringShannonBits(la)
	bLo := StringShannonBits(lo)
	if CountTokens(la) <= TokenTier4 && bLa >= AtomicConceptEntropyBits && bLa <= 2.5*AtomicConceptEntropyBits && bLa <= bLo {
		for _, r := range la {
			if r >= 0x80 && strings.ContainsRune(lo, r) {
				return true
			}
		}
	}
	return false
}

// AliasBasisGate validates the structured basis commitment of an alias binding.
func AliasBasisGate(a AliasSpec) (bool, string) {
	b := strings.ToLower(strings.TrimSpace(a.Basis))
	if b == "" {
		return false, "basis 缺失"
	}
	if !aliasBasisClosedSet[b] {
		return false, "basis 闭集外"
	}
	if b == "shortening" && !IsPlainShortening(a.Of, a.Alias) {
		return false, "shortening 非缩短式"
	}
	if b == "relation" {
		ofTrim := strings.TrimSpace(a.Of)
		alTrim := strings.TrimSpace(a.Alias)
		loOf := strings.ToLower(CanonicalizeApostrophe(ofTrim))
		loAl := strings.ToLower(CanonicalizeApostrophe(alTrim))
		if ofTrim != "" && alTrim != "" &&
			!strings.ContainsAny(alTrim, " \t\n") &&
			!strings.Contains(ofTrim, "的") && !strings.Contains(alTrim, "的") &&
			CountTokens(ofTrim) <= TokenTier4 && CountTokens(alTrim) <= TokenTier4 &&
			!hasPossessiveMarker(loOf) && !hasPossessiveMarker(loAl) &&
			!strings.Contains(loOf, loAl) && !strings.Contains(loAl, loOf) &&
			!IsPlainShortening(ofTrim, alTrim) && !IsPlainShortening(alTrim, ofTrim) {
			return false, "relation 不可绑定两个无前缀重合的独立单字名"
		}
	}
	return true, ""
}

// CorrectAliasTargetByPrefix corrects vocative prefix shortenings (e.g. Caroline saying "Hey Mel!"
// where Mel is a prefix of candidate "Melanie", not speaker "Caroline").
// Enforces high-precision safety ("宁漏不错"): never overwrites `a.Of` if `a.Of` and `a.Alias` already share
// a lexical substring or any CJK ideograph, and uses TrimTrailingGeminateRune rather than StemToken so complete
// 5-letter names (e.g. "Carol") are never truncated into prefixes of longer names ("Caroline").
func CorrectAliasTargetByPrefix(a AliasSpec, candidates ...string) AliasSpec {
	ofName := strings.TrimSpace(a.Of)
	alName := strings.TrimSpace(a.Alias)
	// 架构契约：移除 `StringShannonBits(alName) < 4.5` 资格截断阈值（实测误杀 "Jo"/"Ev" 类真前缀昵称）；
	// 前缀/截断关系由下方逐字形式判据裁决，语义归属 100% 归 LLM。
	if ofName == "" || strings.ContainsAny(alName, " '\u2019") {
		return a
	}
	ofLow := strings.ToLower(ofName)
	alLow := strings.ToLower(alName)
	dedupAlLow := TrimTrailingGeminateRune(alLow)
	if strings.HasPrefix(ofLow, alLow) || strings.HasPrefix(ofLow, dedupAlLow) ||
		strings.Contains(ofLow, alLow) || strings.Contains(alLow, ofLow) ||
		(IsPureCJK(ofName) && IsPureCJK(alName) && sharesAnyRune(ofName, alName)) {
		return a
	}
	seen := map[string]bool{}
	var matched string
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		low := strings.ToLower(cand)
		if cand == "" || strings.ContainsAny(cand, " '\u2019") || seen[low] ||
			strings.EqualFold(cand, ofName) || strings.EqualFold(cand, alName) {
			continue
		}
		seen[low] = true
		if StringShannonBits(low) > StringShannonBits(alLow) &&
			(strings.HasPrefix(low, alLow) || (dedupAlLow != alLow && strings.HasPrefix(low, dedupAlLow))) {
			if matched == "" {
				matched = cand
			} else {
				matched = "\x00ambiguous"
			}
		}
	}
	if matched != "" && matched != "\x00ambiguous" {
		a.Of = matched
		if strings.HasPrefix(strings.ToLower(matched), alLow) {
			a.Basis = "shortening"
		} else {
			a.Basis = "referral"
		}
	}
	return a
}

// IsHollowSocialEdge reports whether an assertion in batch encoding mode is a hollow interpersonal
// greeting/question shell between batch speakers with zero factual payload.
func IsHollowSocialEdge(subject, rel, object, objectLiteral string, subjectRef, objectRef uint64, timeExpr string, tValidFrom int64, participantCount int, batchSpeakers []string) bool {
	if object == "" || objectLiteral != "" || objectRef > 0 || subjectRef > 0 ||
		timeExpr != "" || tValidFrom > 0 || participantCount > 0 {
		return false
	}
	if len(Tokenize(rel)) > 1 {
		return false
	}
	speakers := make(map[string]bool, len(batchSpeakers))
	for _, sp := range batchSpeakers {
		if s := strings.ToLower(strings.TrimSpace(sp)); s != "" {
			speakers[s] = true
		}
	}
	sLow := strings.ToLower(strings.TrimSpace(subject))
	oLow := strings.ToLower(strings.TrimSpace(object))
	return speakers[sLow] && speakers[oLow]
}

// LongestVerbatimSubspan finds the highest-entropy maximal contiguous substring (>=8.0 Shannon bits)
// of anchor that occurs verbatim in turnText using an O(N) two-pointer monotone sliding window.
// Evaluating maximal common substrings prevents periodic floods ("ab ab ab ...") from being artificially
// truncated to a 2-period 8.0-bit fragment ("ab ab") to evade tandem damping.
func LongestVerbatimSubspan(turnText, anchor string) string {
	if turnText == "" || anchor == "" {
		return ""
	}
	ar := []rune(strings.TrimSpace(anchor))
	n := len(ar)
	if n == 0 {
		return ""
	}
	best := ""
	bestBits := 0.0
	j := 0
	prevJ := -1
	for i := 0; i < n; i++ {
		if j < i {
			j = i
		}
		for j < n && strings.Contains(turnText, string(ar[i:j+1])) {
			j++
		}
		if j <= i || j == prevJ {
			continue
		}
		prevJ = j
		cand := strings.TrimSpace(string(ar[i:j]))
		if cand == "" {
			continue
		}
		if bits := StringShannonBits(cand); bits >= 8.0 && bits > bestBits {
			best = cand
			bestBits = bits
		}
	}
	return best
}

// AssertionHash16 computes the 16-slot idempotent SHA-1 hash used by the PostgreSQL store.
func AssertionHash16(scope string, subjectID, relID, objectID, holderID uint64, literal, warrant, turnHash string, bare bool, subjectGap, objectGap, holderGap string) string {
	bareBit := "0"
	if bare {
		bareBit = "1"
	}
	parts := []string{
		scope,
		strconv.FormatUint(subjectID, 10),
		"",
		strconv.FormatUint(relID, 10),
		strconv.FormatUint(objectID, 10),
		strings.TrimSpace(literal),
		bareBit,
		"0",
		"0",
		strconv.FormatUint(holderID, 10),
		warrant,
		turnHash,
		"0:0",
		subjectGap,
		objectGap,
		holderGap,
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// AssertionDigestSHA256 computes a deterministic SHA-256 hex prefix over string-keyed slots.
func AssertionDigestSHA256(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// ValidateUnboundAndDualForm validates the 4-state structured memory assertion morphology (ternary / binary_sx / binary_xo / unbound)
// and returns a non-empty rejection reason if invalid.
func ValidateUnboundAndDualForm(subject string, subjectRef uint64, subjectAbsent bool, subjectGap, rel, object, objectLiteral string, objectRef uint64, objectGap, holder, holderGap string) string {
	sGap := NormalizeGap(subjectGap)
	oGap := NormalizeGap(objectGap)
	hGap := NormalizeGap(holderGap)
	if sGap != "" && !GapSet[sGap] {
		return "subject_gap 不在闭集 {absent_in_source,referent_unresolvable,unattributed,out_of_scope}"
	}
	if oGap != "" && !GapSet[oGap] {
		return "object_gap 不在闭集 {absent_in_source,referent_unresolvable,unattributed,out_of_scope}"
	}
	if hGap != "" && !GapSet[hGap] {
		return "holder_gap 不在闭集 {absent_in_source,referent_unresolvable,unattributed,out_of_scope}"
	}
	if sGap != "" && (strings.TrimSpace(subject) != "" || subjectRef > 0) {
		return "subject 与 subject_gap 互斥（悬空未定：去 subject、留 subject_gap；已指住：去 subject_gap）"
	}
	if subjectAbsent && (strings.TrimSpace(subject) != "" || subjectRef > 0 || sGap != "") {
		return "subject_absent 与 subject / subject_gap 互斥（XO 二元 = 主体不适用，二选一）"
	}
	if oGap != "" && (strings.TrimSpace(object) != "" || strings.TrimSpace(objectLiteral) != "" || objectRef > 0) {
		return "object / object_literal 与 object_gap 互斥（给了客体就不要声明客体未定）"
	}
	if strings.TrimSpace(holder) != "" && hGap != "" {
		return "holder 与 holder_gap 互斥（指住了来源就不要声明来源不明）"
	}
	if strings.TrimSpace(rel) == "" {
		return "rel 必填"
	}
	if sGap == "" && !subjectAbsent && strings.TrimSpace(subject) == "" && subjectRef == 0 {
		return "subject（体验者补位后仍缺）与 rel 必填"
	}
	if len([]rune(rel)) > NodeLabelColumnCap {
		return fmt.Sprintf("rel 超列宽上限（≤%d 字符）", NodeLabelColumnCap)
	}
	if len([]rune(subject)) > NodeLabelColumnCap {
		return fmt.Sprintf("subject 超列宽上限（≤%d 字符）", NodeLabelColumnCap)
	}
	if len([]rune(object)) > NodeLabelColumnCap {
		return fmt.Sprintf("object 超列宽上限（≤%d 字符）", NodeLabelColumnCap)
	}
	return ""
}

func asMapString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprintf("%v", v)
	}
}

func extractSubAssertionMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	if strings.TrimSpace(asMapString(m["rel"])) != "" || strings.TrimSpace(asMapString(m["x"])) != "" {
		return m
	}
	return nil
}

// ExpandInlineSubAssertions performs a 0-LLM post-order AST traversal over a batch of assertions,
// flattening inline sub_assertion / object_assertion objects (or nested assertion maps in object/o or subject/s)
// into atomic assertions placed before the outer assertion and bound via 1-based "@k" references.
func ExpandInlineSubAssertions(arr []map[string]any) []map[string]any {
	hasInline := false
	for _, m := range arr {
		if m == nil {
			continue
		}
		if extractSubAssertionMap(m["sub_assertion"]) != nil ||
			extractSubAssertionMap(m["object_assertion"]) != nil ||
			extractSubAssertionMap(m["object"]) != nil ||
			extractSubAssertionMap(m["o"]) != nil ||
			extractSubAssertionMap(m["subject"]) != nil ||
			extractSubAssertionMap(m["s"]) != nil {
			hasInline = true
			break
		}
	}
	if !hasInline {
		return arr
	}

	out := make([]map[string]any, 0, len(arr)*2)
	origToNew := make(map[int]int, len(arr))

	anchorOf := func(m map[string]any) string {
		if a := strings.TrimSpace(asMapString(m["anchor"])); a != "" {
			return a
		}
		return strings.TrimSpace(asMapString(m["anchor_text"]))
	}

	var emitNode func(m map[string]any, parentSrc any, parentAnchor string) int
	emitNode = func(m map[string]any, parentSrc any, parentAnchor string) int {
		cp := make(map[string]any, len(m))
		for k, v := range m {
			if k != "sub_assertion" && k != "object_assertion" {
				cp[k] = v
			}
		}
		if cp["src"] == nil && parentSrc != nil {
			cp["src"] = parentSrc
		}
		curSrc := cp["src"]
		curAnchor := anchorOf(cp)
		if curAnchor == "" && parentAnchor != "" {
			if _, hasAnchorText := cp["anchor_text"]; hasAnchorText || cp["x"] != nil {
				cp["anchor_text"] = parentAnchor
			} else {
				cp["anchor"] = parentAnchor
			}
			curAnchor = parentAnchor
		}

		var inlineObj map[string]any
		useShortKey := cp["x"] != nil && cp["rel"] == nil
		if sub := extractSubAssertionMap(m["sub_assertion"]); sub != nil {
			inlineObj = sub
		} else if sub := extractSubAssertionMap(m["object_assertion"]); sub != nil {
			inlineObj = sub
		} else if sub := extractSubAssertionMap(m["object"]); sub != nil {
			inlineObj = sub
		} else if sub := extractSubAssertionMap(m["o"]); sub != nil {
			inlineObj = sub
			useShortKey = true
		}
		if inlineObj != nil {
			childIdx := emitNode(inlineObj, curSrc, curAnchor)
			ref := fmt.Sprintf("\x00@%d", childIdx)
			if useShortKey {
				cp["o"] = ref
			} else {
				cp["object"] = ref
				delete(cp, "object_literal")
			}
		}

		if subSubj := extractSubAssertionMap(m["subject"]); subSubj != nil {
			childIdx := emitNode(subSubj, curSrc, curAnchor)
			cp["subject"] = fmt.Sprintf("\x00@%d", childIdx)
		} else if subSubj := extractSubAssertionMap(m["s"]); subSubj != nil {
			childIdx := emitNode(subSubj, curSrc, curAnchor)
			cp["s"] = fmt.Sprintf("\x00@%d", childIdx)
		}

		out = append(out, cp)
		return len(out)
	}

	for i, m := range arr {
		if m == nil {
			out = append(out, nil)
			origToNew[i+1] = len(out)
			continue
		}
		newIdx := emitNode(m, m["src"], anchorOf(m))
		origToNew[i+1] = newIdx
	}

	for _, m := range out {
		if m == nil {
			continue
		}
		for _, slot := range []string{"subject", "s", "object", "o"} {
			v := strings.TrimSpace(asMapString(m[slot]))
			if strings.HasPrefix(v, "\x00@") {
				m[slot] = v[1:]
				continue
			}
			if strings.HasPrefix(v, "@") {
				if origK, err := strconv.Atoi(strings.TrimSpace(v[1:])); err == nil && origK >= 1 {
					if mappedK, exists := origToNew[origK]; exists && mappedK > 0 {
						m[slot] = fmt.Sprintf("@%d", mappedK)
					}
				}
			}
		}
	}

	return out
}

// ResolveBatchAtRefString resolves a "@k" reference at selfIdx (0-based) against batchIDs
// (1-based index k -> batchIDs[k-1]), falling back to the closest preceding atomic assertion
// sharing the same anchor substring or the immediately preceding same-src assertion when @k is out of bounds.
func ResolveBatchAtRefString(ref string, arr []map[string]any, batchIDs []string, selfIdx int, selfAnchor string) string {
	v := strings.TrimSpace(ref)
	if !strings.HasPrefix(v, "@") {
		return ref
	}
	parsedK, kErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(v, "@")))
	if kErr == nil && parsedK >= 1 && parsedK <= len(batchIDs) && parsedK-1 != selfIdx {
		if id := strings.TrimSpace(batchIDs[parsedK-1]); id != "" {
			if strings.HasPrefix(id, "#") {
				return id
			}
			return "#" + id
		}
	}
	if kErr == nil && parsedK-1 == selfIdx {
		return ref
	}
	var selfSrc string
	if selfIdx >= 0 && selfIdx < len(arr) && arr[selfIdx] != nil {
		selfSrc = strings.TrimSpace(asMapString(arr[selfIdx]["src"]))
	}
	sameSrcAt := func(j int) bool {
		if j < 0 || j >= len(arr) || arr[j] == nil {
			return false
		}
		if selfSrc == "" {
			return true
		}
		jSrc := strings.TrimSpace(asMapString(arr[j]["src"]))
		return jSrc == "" || jSrc == selfSrc
	}
	anchorM := strings.TrimSpace(selfAnchor)
	if anchorM != "" {
		for j := selfIdx - 1; j >= 0; j-- {
			if j >= len(batchIDs) || strings.TrimSpace(batchIDs[j]) == "" || !sameSrcAt(j) {
				continue
			}
			mj := arr[j]
			aj := strings.TrimSpace(asMapString(mj["anchor"]))
			if aj == "" {
				aj = strings.TrimSpace(asMapString(mj["anchor_text"]))
			}
			if aj != "" && (strings.Contains(anchorM, aj) || strings.Contains(aj, anchorM)) {
				id := strings.TrimSpace(batchIDs[j])
				if strings.HasPrefix(id, "#") {
					return id
				}
				return "#" + id
			}
		}
	}
	if selfIdx > 0 && selfIdx-1 < len(batchIDs) && sameSrcAt(selfIdx-1) {
		if id := strings.TrimSpace(batchIDs[selfIdx-1]); id != "" {
			if strings.HasPrefix(id, "#") {
				return id
			}
			return "#" + id
		}
	}
	return ref
}

// ConnectivityMetrics quantifies graph connectivity richness and entity/predicate reuse.
type ConnectivityMetrics struct {
	TotalAssertions      int     `json:"total_assertions"`
	ObjectNodeCount      int     `json:"object_node_count"`
	ObjectNodeRatio      float64 `json:"object_node_ratio"`
	EntityNodeCount      int     `json:"entity_node_count"`
	MultiEdgeEntityCount int     `json:"multi_edge_entity_count"`
	AvgEntityDegree      float64 `json:"avg_entity_degree"`
	MultiEdgeEntityRatio float64 `json:"multi_edge_entity_ratio"`
	RelationNodeCount    int     `json:"relation_node_count"`
	AvgRelationReuse     float64 `json:"avg_relation_reuse"`
	PhraseNodeSlots      int     `json:"phrase_node_slots"`
	ClauseReferentSlots  int     `json:"clause_referent_slots"`
}

// ComputeConnectivityMetrics computes deterministic graph connectivity and reuse metrics
// over a set of active assertions and their endpoint labels/kinds.
func ComputeConnectivityMetrics(assertions []Assertion, nodeKinds, nodeLabels map[string]string) ConnectivityMetrics {
	var m ConnectivityMetrics
	m.TotalAssertions = len(assertions)
	if m.TotalAssertions == 0 {
		return m
	}
	entityDeg := make(map[string]int, len(assertions))
	relSet := make(map[string]bool, 32)
	for _, a := range assertions {
		sID := normGraphID(a.SubjectID, a.NumSubjectID)
		rID := normGraphID(a.RelID, a.NumRelID)
		oID := normGraphID(a.ObjectID, a.NumObjectID)
		if sID != "" {
			entityDeg[sID]++
		}
		if oID != "" {
			m.ObjectNodeCount++
			entityDeg[oID]++
		} else if inlineRefs := ExtractInlineNodeRefs(a.ObjectLiteral); len(inlineRefs) > 0 {
			m.ObjectNodeCount++
			for _, refID := range inlineRefs {
				entityDeg[refID]++
			}
		}
		if rID != "" {
			relSet[rID] = true
		}
		sKind := nodeKinds[sID]
		oKind := nodeKinds[oID]
		sToks := CountTokens(nodeLabels[sID])
		oToks := CountTokens(nodeLabels[oID])
		if (sID != "" && (sKind == "" || sKind == "referent") && sToks >= TokenTier4) ||
			(oID != "" && (oKind == "" || oKind == "referent") && oToks >= TokenTier4) {
			m.PhraseNodeSlots++
		}
		if (sID != "" && (sKind == "" || sKind == "referent") && sToks >= 10) ||
			(oID != "" && (oKind == "" || oKind == "referent") && oToks >= 10) {
			m.ClauseReferentSlots++
		}
	}
	m.ObjectNodeRatio = float64(m.ObjectNodeCount) / float64(m.TotalAssertions)
	m.EntityNodeCount = len(entityDeg)
	if m.EntityNodeCount > 0 {
		totalDeg := 0
		multiCnt := 0
		for _, d := range entityDeg {
			totalDeg += d
			if d >= 2 {
				multiCnt++
			}
		}
		m.MultiEdgeEntityCount = multiCnt
		m.AvgEntityDegree = float64(totalDeg) / float64(m.EntityNodeCount)
		m.MultiEdgeEntityRatio = float64(multiCnt) / float64(m.EntityNodeCount)
	}
	m.RelationNodeCount = len(relSet)
	if m.RelationNodeCount > 0 {
		m.AvgRelationReuse = float64(m.TotalAssertions) / float64(m.RelationNodeCount)
	}
	return m
}

// SelectPreProbeConnectivityHints deduplicates and caps canonical entity names (up to maxEntities)
// and ranks existing relation predicates by reuse frequency descending (up to maxRelations)
// for 0-LLM pre-write graph probe guidance.
func SelectPreProbeConnectivityHints(entityLabels []string, relCounts map[string]int, maxEntities, maxRelations int) (entities, relations []string) {
	if maxEntities <= 0 {
		maxEntities = 20
	}
	if maxRelations <= 0 {
		maxRelations = 16
	}
	seenEnt := make(map[string]bool, len(entityLabels))
	for _, raw := range entityLabels {
		lb := strings.TrimSpace(raw)
		if lb == "" {
			continue
		}
		key := strings.ToLower(lb)
		if !seenEnt[key] {
			seenEnt[key] = true
			entities = append(entities, lb)
			if len(entities) >= maxEntities {
				break
			}
		}
	}
	for rel, cnt := range relCounts {
		r := strings.TrimSpace(rel)
		if r != "" && cnt > 0 {
			relations = append(relations, r)
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		ci, cj := relCounts[relations[i]], relCounts[relations[j]]
		if ci != cj {
			return ci > cj
		}
		return relations[i] < relations[j]
	})
	if len(relations) > maxRelations {
		relations = relations[:maxRelations]
	}
	return entities, relations
}

// MinEvidenceQuorumRows 最小证据量闸（架构契约 · 完美标准级证伪根治）：
// max(断言数, 源轮数) 低于该值的微样本域禁评 S 级——满分也只能封顶 A（样本不足），
// 杜绝 n=1 域冒充「Theoretically Perfect」（读侧配额口径沿用）。
const MinEvidenceQuorumRows = 32
