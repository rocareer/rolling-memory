// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"github.com/rocareer/rolling-memory/internal/text"
)

// DefaultHashDim is the feature-hash vector dimension (delegates to internal/text).
const DefaultHashDim = text.DefaultHashDim

// FeatureHashVec computes the L2-normalized feature-hash vector (delegates to internal/text).
func FeatureHashVec(s string, dim int) []float32 { return text.FeatureHashVec(s, dim) }

// DotUnit returns the dot product of two unit vectors (delegates to internal/text).
func DotUnit(a, b []float32) float64 { return text.DotUnit(a, b) }

// Cosine returns the cosine similarity of two vectors (delegates to internal/text).
func Cosine(a, b []float32) float64 { return text.Cosine(a, b) }
