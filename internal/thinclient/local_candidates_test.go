// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

type candidateTestStore struct {
	PlaintextStore
	missing map[string]bool
	cancel  context.CancelFunc
	loads   int
}

func (s *candidateTestStore) LoadLocalDocument(id string) (string, string, []string, error) {
	s.loads++
	if s.cancel != nil {
		s.cancel()
	}
	if s.missing[id] {
		return "", "", nil, errors.New("source unavailable")
	}
	return id, id, nil, nil
}

func TestLocalCandidateLimitPreservesGaps(t *testing.T) {
	hits := []LocalResolvedHit{
		{CloudQueryHit: CloudQueryHit{UnitID: "missing"}, SourceUnavailable: true},
		{CloudQueryHit: CloudQueryHit{UnitID: "first"}},
		{CloudQueryHit: CloudQueryHit{UnitID: "second"}},
	}
	got := limitHydratedLocalHits(hits, 1)
	if len(got) != 2 || got[0].UnitID != "missing" || got[1].UnitID != "first" {
		t.Fatalf("limited hits: %+v", got)
	}
}

func TestLocalCandidateHydrationContext(t *testing.T) {
	db := migrationTestDB(t)
	vault, err := NewVault(db, []byte("candidate-hydration-cancel"))
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.SaveLocalPlaintext("doc", 1, "quartz nebula", nil, CloudIngestResponse{Layer: LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := &candidateTestStore{PlaintextStore: vault, cancel: cancel}
	client := NewClientWithStore(store, NewLocalVaultTransport(vault))
	_, err = client.Recall(ctx, "quartz nebula", 2)
	if !errors.Is(err, context.Canceled) || store.loads != 1 {
		t.Fatalf("loads=%d err=%v", store.loads, err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = client.Recall(ctx, "quartz nebula", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestLocalTransportCanceledContext(t *testing.T) {
	db := migrationTestDB(t)
	vault, err := NewVault(db, []byte("candidate-cancel"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(vault, NewLocalVaultTransport(vault))
	_, err = client.Recall(ctx, "quartz", 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recall canceled: %v", err)
	}
}
