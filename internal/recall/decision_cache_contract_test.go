// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import "testing"

func TestDecisionCacheSeparatesSessionPresence(t *testing.T) {
	empty := ""
	named := "nil"
	base := Options{Reader: "owner"}
	unfiltered := DecisionCacheKey(nil, "mem:cache:session", "witness", base)
	base.Session = &empty
	emptySession := DecisionCacheKey(nil, "mem:cache:session", "witness", base)
	base.Session = &named
	namedSession := DecisionCacheKey(nil, "mem:cache:session", "witness", base)
	if unfiltered == emptySession || unfiltered == namedSession || emptySession == namedSession {
		t.Fatal("session presence and value must produce distinct decision cache keys")
	}
	DecisionCacheReset()
	t.Cleanup(DecisionCacheReset)
	DecisionCachePut(unfiltered, &Result{Bands: []map[string]any{{"subject": "unfiltered"}}})
	if _, ok := DecisionCacheGet(emptySession); ok {
		t.Fatal("empty-session query reused an unfiltered result")
	}
}
