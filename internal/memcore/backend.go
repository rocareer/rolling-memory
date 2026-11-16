// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import "context"

// StoreBackend defines the database-agnostic storage contract implemented identically by
// PostgreSQL (server mode) and SQLite (Rolling-Memory local mode).
//
// Dialect differences (e.g. PostgreSQL ON CONFLICT / pgvector vs SQLite IMMEDIATE tx / FTS5 trigram)
// are strictly encapsulated behind this interface so all higher-level memory capabilities remain 1:1.
type StoreBackend interface {
	// Dialect returns the backend identifier ("postgres" or "sqlite").
	Dialect() string

	// EnsureNode finds or creates a node by (scope, kind, label), returning (node, created, error).
	EnsureNode(ctx context.Context, scope, kind, label, audience, world string, now int64) (*Node, bool, error)

	// FindNodeByName resolves a referent node by exact case-insensitive label or registered alias.
	FindNodeByName(ctx context.Context, scope, name string) (*Node, bool, error)

	// BindAlias registers an alias for a node if not already bound to another live node in the same scope.
	BindAlias(ctx context.Context, scope, nodeID, alias, source string, now int64) (bool, error)

	// AppendTrace persists an L0 verbatim evidence trace idempotently.
	AppendTrace(ctx context.Context, t Trace) (*Trace, bool, error)

	// AppendAssertion persists a structured memory assertion row (plus unbound slot / event-time metadata) idempotently.
	AppendAssertion(ctx context.Context, a Assertion, assertionLabel string) (*Assertion, bool, error)

	// ClosePriorAssertions marks older active assertions of the same (scope, subject_id, rel_id) with a different value
	// as superseded and sets t_valid_to = validTo, recording supersedes meta_edges.
	ClosePriorAssertions(ctx context.Context, scope, newAssertionID, subjectID, relID, objectID, objectLiteral string, validTo, now int64) ([]string, error)

	// RecordRestate appends an occurrence or mention entry into the restate ledger.
	RecordRestate(ctx context.Context, r RestateRow) error

	// RecordMemoryHot appends a recall or experience entry into the two-zone working hot memory ledger.
	RecordMemoryHot(ctx context.Context, h MemoryHot) error

	// RecentMemoryHot retrieves the most recent hot memory entries for a scope (and optional sessionKey).
	RecentMemoryHot(ctx context.Context, scope, sessionKey string, limit int) ([]MemoryHot, error)

	// UpsertEpisodeContainer creates or updates an episode universe container and its members.
	UpsertEpisodeContainer(ctx context.Context, c EpisodeContainer, members []EpisodeMember) error

	// ListEpisodeContainers lists episode containers for a scope.
	ListEpisodeContainers(ctx context.Context, scope string, level int, limit int) ([]EpisodeContainer, error)
}
