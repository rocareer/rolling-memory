// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// DefaultHashDim is the default feature-hashing vector dimension (1024-dim L2 unit sphere).
const DefaultHashDim = 1024

// FeatureHashVec projects text character 1/2/3-grams and word segments onto a dim-dimensional
// L2 unit sphere using deterministic signed FNV-1a feature hashing (0-LLM, language-agnostic).
func FeatureHashVec(text string, dim int) []float32 {
	if dim <= 0 {
		dim = DefaultHashDim
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	vec := make([]float32, dim)
	runes := []rune(text)
	isSem := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
	}
	addFeat := func(feat string, weight float32) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(feat))
		sum := h.Sum64()
		idx := int(sum % uint64(dim))
		sign := float32(1.0)
		if (sum>>63)&1 == 1 {
			sign = -1.0
		}
		vec[idx] += sign * weight
	}
	cleanRunes := make([]rune, 0, len(runes))
	for _, r := range runes {
		if isSem(r) {
			cleanRunes = append(cleanRunes, r)
		}
	}
	if len(cleanRunes) == 0 {
		return nil
	}
	for i := 0; i < len(cleanRunes); i++ {
		addFeat("1:"+string(cleanRunes[i:i+1]), 0.35)
		if i+1 < len(cleanRunes) {
			addFeat("2:"+string(cleanRunes[i:i+2]), 1.0)
		}
		if i+2 < len(cleanRunes) {
			addFeat("3:"+string(cleanRunes[i:i+3]), 1.35)
		}
	}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !isSem(r)
	})
	for _, f := range fields {
		if f != "" {
			addFeat("w:"+f, 1.5)
		}
	}
	normalizeVec32InPlace(vec)
	return vec
}

func normalizeVec32InPlace(vec []float32) {
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	if sumSq <= 1e-24 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sumSq))
	for i := range vec {
		vec[i] *= inv
	}
}

// DotUnit computes the inner product of two float32 unit vectors.
func DotUnit(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// Cosine computes the cosine similarity between two float32 vectors in [-1, 1].
func Cosine(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		va, vb := float64(a[i]), float64(b[i])
		dot += va * vb
		na += va * va
		nb += vb * vb
	}
	if na <= 1e-24 || nb <= 1e-24 {
		return 0
	}
	c := dot / math.Sqrt(na*nb)
	if c > 1.0 {
		return 1.0
	}
	if c < -1.0 {
		return -1.0
	}
	return c
}
