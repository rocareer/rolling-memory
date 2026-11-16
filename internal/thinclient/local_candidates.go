// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

func limitHydratedLocalHits(hits []LocalResolvedHit, topK int) []LocalResolvedHit {
	usable := 0
	out := hits[:0]
	for _, hit := range hits {
		if hit.SourceUnavailable {
			out = append(out, hit)
		} else if usable < topK {
			out = append(out, hit)
			usable++
		}
	}
	return out
}
