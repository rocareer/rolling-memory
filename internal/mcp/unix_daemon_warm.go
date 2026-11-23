// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"context"
	"time"

	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

// publishedWarmInterval bounds how long a freshly published generation can stay
// unopened. The background builder publishes on the order of once a minute; a
// short poll keeps the first recall after a publication from paying the segment
// open cost while costing one pointer-file read per tick.
const publishedWarmInterval = 2 * time.Second

// warmPublishedGenerations keeps the newest published cluster opened ahead of
// the query path. It never blocks serving: each tick is a cheap CURRENT probe
// and the open itself runs on this goroutine.
func warmPublishedGenerations(ctx context.Context, vault *localvault.Vault, interval time.Duration) {
	if vault == nil {
		return
	}
	if interval <= 0 {
		interval = publishedWarmInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	last := ""
	for {
		slot, ok := vault.PublishedSlot()
		if ok && slot != last {
			if warmed, changed, err := vault.WarmPublishedGeneration(ctx); err == nil && changed && warmed != "" {
				last = warmed
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
