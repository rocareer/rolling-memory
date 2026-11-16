// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"context"
	"strings"

	"github.com/rocareer/rolling-memory/internal/text"
)

type ctxPresenceCoordsKey struct{}
type ctxAudiencePeersKey struct{}

// PresenceCoordinates is a type alias to text.PresenceCoordinates to maintain
// backward compatibility while we refactor core primitives to the new package.
type PresenceCoordinates = text.PresenceCoordinates

// IsEmpty reports whether all coordinates are unset.
func IsEmpty(p PresenceCoordinates) bool {
	return p.World == "" && p.Carrier == "" && p.Locus == "" && p.Incarnation == "" && p.SelfLabel == "" && p.PeerLabel == ""
}

// Normalized returns a copy with normalized hierarchical paths and trimmed fields.
func Normalized(p PresenceCoordinates) PresenceCoordinates {
	return text.PresenceCoordinates{
		World:       strings.TrimSpace(p.World),
		Carrier:     strings.TrimSpace(p.Carrier),
		Locus:       NormalizeHierarchicalPath(p.Locus),
		Incarnation: NormalizeHierarchicalPath(p.Incarnation),
		SelfLabel:   strings.TrimSpace(p.SelfLabel),
		PeerLabel:   strings.TrimSpace(p.PeerLabel),
	}
}

// WithPresenceCoordinates binds PresenceCoordinates onto ctx.
func WithPresenceCoordinates(ctx context.Context, coords PresenceCoordinates) context.Context {
	return text.WithPresenceCoordinates(ctx, coords)
}

// PresenceCoordinatesFromCtx extracts PresenceCoordinates bound on ctx.
func PresenceCoordinatesFromCtx(ctx context.Context) (PresenceCoordinates, bool) {
	return text.PresenceCoordinatesFromCtx(ctx)
}

// WithAudiencePeers binds the active audience peer subjects onto ctx.
func WithAudiencePeers(ctx context.Context, peers ...string) context.Context {
	return text.WithAudiencePeers(ctx, peers...)
}

// AudiencePeersFromCtx returns the active audience peer subjects bound on ctx.
func AudiencePeersFromCtx(ctx context.Context) []string {
	return text.AudiencePeersFromCtx(ctx)
}

// NormalizeHierarchicalPath normalizes a '/' or '\' separated hierarchical path,
// stripping redundant separators, '.', and folding '..' parent segments without domain-specific word lists.
func NormalizeHierarchicalPath(raw string) string {
	return text.NormalizeHierarchicalPath(raw)
}

// SplitHierarchicalSegments splits a hierarchical path into non-empty segments.
// ---- 时空坐标 / 工作区存在探测原语：唯一正本在 internal/text（薄转发）----

// 时空共振软势场权重（非退化：乘子 >= 1.0，delegates to internal/text）。
const (
	SpacetimeAlphaLocus   = text.SpacetimeAlphaLocus
	SpacetimeAlphaCarrier = text.SpacetimeAlphaCarrier
	SpacetimeBetaInc      = text.SpacetimeBetaInc
)

// SplitHierarchicalSegments splits a hierarchical path into non-empty segments (delegates to internal/text).
func SplitHierarchicalSegments(raw string) []string { return text.SplitHierarchicalSegments(raw) }

// HierarchicalLCPScore computes the language-agnostic hierarchical tree LCP affinity kernel (delegates to internal/text).
func HierarchicalLCPScore(pathA, pathB string) float64 {
	return text.HierarchicalLCPScore(pathA, pathB)
}

// IncarnationGenealogyScore computes the incarnation role hierarchy affinity kernel (delegates to internal/text).
func IncarnationGenealogyScore(incQuery, incAssertion string) float64 {
	return text.IncarnationGenealogyScore(incQuery, incAssertion)
}

// SpacetimeResonanceMultiplier computes the soft potential field multiplier (delegates to internal/text).
func SpacetimeResonanceMultiplier(query PresenceCoordinates, rowLocus, rowCarrier, rowIncarnation string) float64 {
	return text.SpacetimeResonanceMultiplier(query, rowLocus, rowCarrier, rowIncarnation)
}

// CarrierMatches reports whether two carrier channels match (delegates to internal/text).
func CarrierMatches(a, b string) bool { return text.CarrierMatches(a, b) }

// CanonicalSchemaCarrier canonicalizes a schema carrier string (delegates to internal/text).
func CanonicalSchemaCarrier(raw string) string { return text.CanonicalSchemaCarrier(raw) }

// DualTrackLocusPrefixes returns dual-track locus prefixes (delegates to internal/text).
func DualTrackLocusPrefixes(rawLocus string) (prefixes []string, leafShortAlias string) {
	return text.DualTrackLocusPrefixes(rawLocus)
}

// IncarnationHierarchyPrefixes returns incarnation hierarchy prefixes (delegates to internal/text).
func IncarnationHierarchyPrefixes(rawIncarnation string) []string {
	return text.IncarnationHierarchyPrefixes(rawIncarnation)
}

// ResolveEffectiveLocus resolves the effective locus (delegates to internal/text).
func ResolveEffectiveLocus(explicitEventLocus, presenceLocus string, maxRunes int) string {
	return text.ResolveEffectiveLocus(explicitEventLocus, presenceLocus, maxRunes)
}

// ParseGitRemoteSlug parses a git remote URL into a slug (delegates to internal/text).
func ParseGitRemoteSlug(remoteURL string) string { return text.ParseGitRemoteSlug(remoteURL) }

// DetectGitWorkspaceLocus detects the git workspace locus (delegates to internal/text).
func DetectGitWorkspaceLocus(cwd string) string { return text.DetectGitWorkspaceLocus(cwd) }

// DetectHarnessPresence detects harness presence coordinates (delegates to internal/text).
func DetectHarnessPresence(cwd, harnessName, subrole string) PresenceCoordinates {
	return text.DetectHarnessPresence(cwd, harnessName, subrole)
}

// DetectGitWorkspaceUser detects the git workspace user (delegates to internal/text).
func DetectGitWorkspaceUser(cwd string) string { return text.DetectGitWorkspaceUser(cwd) }

// CanonicalAssistantScopeVariants returns canonical assistant scope variants (delegates to internal/text).
func CanonicalAssistantScopeVariants(scope, assistantID string) []string {
	return text.CanonicalAssistantScopeVariants(scope, assistantID)
}
