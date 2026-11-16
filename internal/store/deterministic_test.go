// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"reflect"
	"testing"
)

func TestDeterministicIDsAcrossDatabases(t *testing.T) {
	left := newTestStore(t, testScope)
	right := newTestStore(t, testScope)
	leftReceipt := addFact(t, left, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{TValidFrom: 1772409600})
	rightReceipt := addFact(t, right, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{TValidFrom: 1772409600})
	for _, key := range []string{"id", "hash", "audience", "anchor", "t_valid_from", "confidence"} {
		if !reflect.DeepEqual(leftReceipt[key], rightReceipt[key]) {
			t.Fatalf("%s: left=%v right=%v", key, leftReceipt[key], rightReceipt[key])
		}
	}
	leftTrace := queryText(t, left, "SELECT hash FROM trace")
	rightTrace := queryText(t, right, "SELECT hash FROM trace")
	if leftTrace != rightTrace {
		t.Fatalf("trace hashes differ: %q vs %q", leftTrace, rightTrace)
	}
	leftNode := queryText(t, left, "SELECT node_id FROM assertion")
	rightNode := queryText(t, right, "SELECT node_id FROM assertion")
	if leftNode != rightNode {
		t.Fatalf("assertion node ids differ: %q vs %q", leftNode, rightNode)
	}
}

func TestSameInputSameRows(t *testing.T) {
	first := newTestStore(t, testScope)
	second := newTestStore(t, testScope)
	operations := []struct {
		text, subject, relation, object, anchor string
	}{
		{"老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫"},
		{"老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗"},
		{"老张住在北京。", "老张", "住在", "北京", "老张住在北京"},
	}
	for _, store := range []*Store{first, second} {
		for _, operation := range operations {
			addFact(t, store, testScope, operation.text, operation.subject, operation.relation, operation.object, operation.anchor, factOptions{})
		}
	}
	left, err := first.ExportScope(testScope)
	if err != nil {
		t.Fatalf("export left: %v", err)
	}
	right, err := second.ExportScope(testScope)
	if err != nil {
		t.Fatalf("export right: %v", err)
	}
	if !reflect.DeepEqual(left, right) {
		for _, key := range []string{"traces", "nodes", "assertions", "meta_edges", "events"} {
			if !reflect.DeepEqual(left[key], right[key]) {
				t.Fatalf("%s differs", key)
			}
		}
		t.Fatalf("exports differ")
	}
}

func TestDuplicateTraceIsIdempotent(t *testing.T) {
	store := newTestStore(t, testScope)
	firstHash, firstInserted, err := store.AddTrace(testScope, "老张养了一只猫。", TraceOptions{Author: "user"})
	if err != nil || !firstInserted {
		t.Fatalf("first trace = %v/%v err=%v", firstHash, firstInserted, err)
	}
	secondHash, secondInserted, err := store.AddTrace(testScope, "老张养了一只猫。", TraceOptions{Author: "user"})
	if err != nil {
		t.Fatalf("second trace: %v", err)
	}
	if secondHash != firstHash || secondInserted {
		t.Fatalf("second trace = %v/%v, want %v/false", secondHash, secondInserted, firstHash)
	}
	if countRows(t, store, "trace") != 1 {
		t.Fatalf("trace count = %d", countRows(t, store, "trace"))
	}
}

func TestSearchSeedsOrderIsDeterministic(t *testing.T) {
	store := newTestStore(t, testScope)
	for _, label := range []string{"Alice", "Alicia", "Alicia Smith", "Alicia 2"} {
		if _, err := store.EnsureNode(testScope, "referent", label, NodeOptions{}); err != nil {
			t.Fatalf("ensure %s: %v", label, err)
		}
	}
	if _, _, err := store.SearchSeeds("Alicia", SeedOptions{Scope: testScope, Reader: "owner", Limit: 10}); err != nil {
		t.Fatalf("warm search: %v", err)
	}
	first, legs, err := store.SearchSeeds("Alicia", SeedOptions{Scope: testScope, Reader: "owner", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	second, _, err := store.SearchSeeds("Alicia", SeedOptions{Scope: testScope, Reader: "owner", Limit: 10})
	if err != nil {
		t.Fatalf("search again: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("search order changed")
	}
	for index := 1; index < len(first); index++ {
		previous := first[index-1]
		current := first[index]
		if previous.Score < current.Score ||
			(previous.Score == current.Score && (previous.Label > current.Label ||
				(previous.Label == current.Label && previous.NodeID > current.NodeID))) {
			t.Fatalf("order violated at %d: %v then %v", index, previous, current)
		}
	}
	if legs["exact"] == 0 {
		t.Fatalf("legs = %v", legs)
	}
}
