// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
)

var resolveOpsClosedSet = map[string]bool{
	"alias":    true,
	"roster":   true,
	"neighbor": true,
	"history":  true,
}

// Resolve is the zero-LLM, read-only single-hop resolver (aligned with the service engine's service/memory/resolve.go · A7):
//   - op = "alias":    resolves candidate referent/relation nodes by name or alias and returns their registered aliases;
//   - op = "roster":   resolves identity/roster cross-references (origin, ext_ref, canonical name, alias closure);
//   - op = "neighbor": returns 1-hop outgoing and incoming assertion edges plus relation-type counts for a target node;
//   - op = "history":  returns the chronological assertion history (active + superseded) for a subject node.
func (s *Store) Resolve(scope, op, target string, limit int) (map[string]any, error) {
	op = strings.ToLower(strings.TrimSpace(op))
	if !resolveOpsClosedSet[op] {
		return nil, fmt.Errorf("resolve: unknown op %q (must be one of: alias, roster, neighbor, history)", op)
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = s.Config.Scope
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("resolve: op=%s requires a non-empty target", op)
	}
	_ = db.EnsureMemcoreSchema(s.conn)

	resolveTargetNodeID := func(raw string) (string, string, error) {
		if n, found, err := s.Node(raw); err != nil {
			return "", "", err
		} else if found {
			return n.ID, n.Label, nil
		}
		if n, found, err := s.NodeByLabel(scope, raw, ""); err != nil {
			return "", "", err
		} else if found {
			return n.ID, n.Label, nil
		}
		return "", "", nil
	}

	switch op {
	case "alias", "roster":
		candLimit := limit
		if candLimit <= 0 {
			candLimit = 5
		}
		norm := ids.NormalizeLabel(target)
		pattern := "%" + ids.EscapeLike(norm) + "%"
		rows, err := queryMaps(s.conn,
			`SELECT n.id, n.kind, n.label, n.status, COALESCE(ne.origin, '') AS origin, COALESCE(ne.ext_ref, '') AS ext_ref
			 FROM node n
			 LEFT JOIN node_ext ne ON ne.node_id = n.id AND ne.scope = n.scope
			 WHERE n.scope = ? AND n.kind != 'assertion' AND n.status IN ('provisional', 'active')
			   AND (n.norm_label = ? OR n.norm_label LIKE ? ESCAPE '\'
			        OR EXISTS (SELECT 1 FROM node_alias a WHERE a.node_id = n.id AND (a.norm_alias = ? OR a.norm_alias LIKE ? ESCAPE '\')))
			 ORDER BY (n.norm_label = ?) DESC, n.label ASC, n.id ASC
			 LIMIT ?`,
			scope, norm, pattern, norm, pattern, norm, candLimit)
		if err != nil {
			return nil, err
		}
		candidates := make([]map[string]any, 0, len(rows))
		aliasesByNode := map[string][]string{}
		for _, r := range rows {
			nid := mapString(r, "id")
			al, _ := s.Aliases(nid)
			if len(al) > 0 {
				aliasesByNode[nid] = al
			}
			candidates = append(candidates, map[string]any{
				"node_id": nid,
				"kind":    mapString(r, "kind"),
				"label":   mapString(r, "label"),
				"status":  mapString(r, "status"),
				"origin":  mapString(r, "origin"),
				"ext_ref": mapString(r, "ext_ref"),
			})
		}
		out := map[string]any{
			"ok":         true,
			"op":         op,
			"scope":      scope,
			"query":      target,
			"count":      len(candidates),
			"candidates": candidates,
			"aliases":    aliasesByNode,
		}
		if op == "roster" {
			roster := map[string]any{"found": false}
			if len(candidates) == 1 {
				c0 := candidates[0]
				roster = map[string]any{
					"found":          true,
					"ambiguous":      false,
					"node_id":        c0["node_id"],
					"canonical_name": c0["label"],
					"origin":         c0["origin"],
					"ext_ref":        c0["ext_ref"],
				}
			} else if len(candidates) > 1 {
				names := make([]string, 0, len(candidates))
				for _, c := range candidates {
					names = append(names, mapString(c, "label"))
				}
				roster = map[string]any{
					"found":             false,
					"ambiguous":         true,
					"roster_candidates": names,
				}
			}
			out["roster"] = roster
		}
		return out, nil

	case "neighbor":
		nodeID, nodeLabel, err := resolveTargetNodeID(target)
		if err != nil {
			return nil, err
		}
		if nodeID == "" {
			return nil, fmt.Errorf("resolve: target not found: %s", target)
		}
		rows, err := queryMaps(s.conn,
			`SELECT a.node_id, a.subject_id, COALESCE(ns.label, a.subject_literal, '') AS s_label,
			        a.rel_id, COALESCE(nx.label, '') AS x_label,
			        a.object_id, COALESCE(no.label, a.object_literal, '') AS o_label,
			        a.t_valid_from, a.t_source, a.status
			 FROM assertion a
			 LEFT JOIN node ns ON ns.id = a.subject_id
			 JOIN node nx ON nx.id = a.rel_id
			 LEFT JOIN node no ON no.id = a.object_id
			 WHERE a.scope = ? AND a.status IN ('active', 'superseded')
			   AND (a.subject_id = ? OR a.object_id = ?)
			 ORDER BY (a.status = 'active') DESC, a.t_source DESC, a.node_id ASC
			 LIMIT 200`,
			scope, nodeID, nodeID)
		if err != nil {
			return nil, err
		}
		outgoing := []map[string]any{}
		incoming := []map[string]any{}
		relCount := map[string]int{}
		seen := map[string]bool{}
		for _, r := range rows {
			sid := mapString(r, "subject_id")
			oid := mapString(r, "object_id")
			rel := mapString(r, "x_label")
			if sid == nodeID {
				relCount[rel]++
				oLb := mapString(r, "o_label")
				key := "o:" + oid + ":" + oLb + ":" + rel
				if !seen[key] {
					seen[key] = true
					outgoing = append(outgoing, map[string]any{
						"node_id":      oid,
						"label":        oLb,
						"rel":          rel,
						"t_valid_from": mapInt64(r, "t_valid_from"),
						"t_source":     mapInt64(r, "t_source"),
						"status":       mapString(r, "status"),
					})
				}
			}
			if oid == nodeID {
				sLb := mapString(r, "s_label")
				key := "i:" + sid + ":" + sLb + ":" + rel
				if !seen[key] {
					seen[key] = true
					incoming = append(incoming, map[string]any{
						"node_id":      sid,
						"label":        sLb,
						"rel":          rel,
						"t_valid_from": mapInt64(r, "t_valid_from"),
						"t_source":     mapInt64(r, "t_source"),
						"status":       mapString(r, "status"),
					})
				}
			}
		}
		type relEntry struct {
			Rel string
			N   int
		}
		relSlice := make([]relEntry, 0, len(relCount))
		for rel, n := range relCount {
			relSlice = append(relSlice, relEntry{Rel: rel, N: n})
		}
		sort.Slice(relSlice, func(i, j int) bool {
			if relSlice[i].N != relSlice[j].N {
				return relSlice[i].N > relSlice[j].N
			}
			return relSlice[i].Rel < relSlice[j].Rel
		})
		relations := make([]map[string]any, 0, len(relSlice))
		for _, re := range relSlice {
			relations = append(relations, map[string]any{"rel": re.Rel, "n": re.N})
		}
		truncated := false
		if limit > 0 {
			if len(outgoing) > limit {
				outgoing = outgoing[:limit]
				truncated = true
			}
			if len(incoming) > limit {
				incoming = incoming[:limit]
				truncated = true
			}
		}
		return map[string]any{
			"ok":        true,
			"op":        op,
			"scope":     scope,
			"node_id":   nodeID,
			"label":     nodeLabel,
			"count":     len(outgoing) + len(incoming),
			"outgoing":  outgoing,
			"incoming":  incoming,
			"relations": relations,
			"truncated": truncated,
		}, nil

	case "history":
		nodeID, nodeLabel, err := resolveTargetNodeID(target)
		if err != nil {
			return nil, err
		}
		if nodeID == "" {
			return nil, fmt.Errorf("resolve: target not found: %s", target)
		}
		histLimit := limit
		if histLimit <= 0 {
			histLimit = 50
		}
		rows, err := queryMaps(s.conn,
			`SELECT a.node_id, COALESCE(nx.label, '') AS x_label,
			        COALESCE(no.label, a.object_literal, '') AS o_label,
			        a.status, a.t_valid_from, a.t_valid_to, a.t_source
			 FROM assertion a
			 JOIN node nx ON nx.id = a.rel_id
			 LEFT JOIN node no ON no.id = a.object_id
			 WHERE a.scope = ? AND a.subject_id = ? AND a.status IN ('active', 'superseded')
			 ORDER BY (a.status = 'active') DESC, a.t_source DESC, a.node_id ASC
			 LIMIT ?`,
			scope, nodeID, histLimit)
		if err != nil {
			return nil, err
		}
		history := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			history = append(history, map[string]any{
				"node_id":      mapString(r, "node_id"),
				"rel":          mapString(r, "x_label"),
				"object":       mapString(r, "o_label"),
				"status":       mapString(r, "status"),
				"t_valid_from": mapInt64(r, "t_valid_from"),
				"t_valid_to":   mapInt64(r, "t_valid_to"),
				"t_source":     mapInt64(r, "t_source"),
			})
		}
		return map[string]any{
			"ok":      true,
			"op":      op,
			"scope":   scope,
			"node_id": nodeID,
			"label":   nodeLabel,
			"count":   len(history),
			"history": history,
		}, nil
	}
	return nil, nil
}
