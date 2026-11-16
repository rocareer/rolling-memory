// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"github.com/rocareer/rolling-memory/internal/text"
)

// ExtractStrangerChannelSuffix extracts a channel suffix marker (delegates to internal/text).
func ExtractStrangerChannelSuffix(raw string) (string, bool) {
	return text.ExtractStrangerChannelSuffix(raw)
}

// FormatAnonymousStrangerLabel formats an anonymous stranger label (delegates to internal/text).
func FormatAnonymousStrangerLabel(suffix string, preferCJK bool) string {
	return text.FormatAnonymousStrangerLabel(suffix, preferCJK)
}

// IsAnonymousStrangerLabel reports whether s is an anonymous stranger label (delegates to internal/text).
func IsAnonymousStrangerLabel(s string) bool { return text.IsAnonymousStrangerLabel(s) }
