// Package app holds bqui's UI-agnostic state and view models. Nothing here
// imports AppKit, so it is fully testable on Linux.
package app

import (
	"sync"
)

// NodeKind classifies explorer tree nodes.
type NodeKind int

// Explorer node kinds.
const (
	KindRoot NodeKind = iota
	KindSection
	KindProject
	KindDataset
	KindTable
	KindView
	KindMaterializedView
	KindExternalTable
	KindSnapshot
	KindRoutine
	KindModel
	KindPlaceholder // "Loading…", errors, empty markers
)

var kindNames = [...]string{
	KindRoot:             "root",
	KindSection:          "section",
	KindProject:          "project",
	KindDataset:          "dataset",
	KindTable:            "table",
	KindView:             "view",
	KindMaterializedView: "materialized view",
	KindExternalTable:    "external table",
	KindSnapshot:         "snapshot",
	KindRoutine:          "routine",
	KindModel:            "model",
	KindPlaceholder:      "placeholder",
}

func (k NodeKind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}

	return "unknown"
}

// Container reports whether nodes of this kind can have children.
func (k NodeKind) Container() bool {
	switch k {
	case KindRoot, KindSection, KindProject, KindDataset:
		return true
	case KindTable, KindView, KindMaterializedView, KindExternalTable,
		KindSnapshot, KindRoutine, KindModel, KindPlaceholder:
	}

	return false
}

// RootID is the handle of the invisible tree root.
const RootID uint64 = 0

// Node is one explorer tree entry. Values returned by Tree are copies.
type Node struct {
	ID       uint64
	Parent   uint64
	Kind     NodeKind
	Title    string
	Detail   string // tooltip / secondary text
	Children []uint64
}

// Tree is a concurrency-safe tree with stable, small integer handles, suitable
// for backing an outline view.
type Tree struct {
	mu    sync.RWMutex
	nodes map[uint64]*Node
	next  uint64
}

// NewTree returns an empty tree containing only the root.
func NewTree() *Tree {
	return &Tree{
		nodes: map[uint64]*Node{RootID: {ID: RootID, Kind: KindRoot}},
		next:  RootID + 1,
	}
}

// Add appends a child under parent and returns its handle. It returns 0 if
// parent does not exist.
func (t *Tree) Add(parent uint64, kind NodeKind, title, detail string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	p, ok := t.nodes[parent]
	if !ok {
		return 0
	}

	id := t.next
	t.next++
	t.nodes[id] = &Node{ID: id, Parent: parent, Kind: kind, Title: title, Detail: detail}
	p.Children = append(p.Children, id)

	return id
}

// Clear removes all descendants of id, keeping id itself.
func (t *Tree) Clear(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	n, ok := t.nodes[id]
	if !ok {
		return
	}

	for _, c := range n.Children {
		t.removeLocked(c)
	}

	n.Children = nil
}

func (t *Tree) removeLocked(id uint64) {
	n, ok := t.nodes[id]
	if !ok {
		return
	}

	for _, c := range n.Children {
		t.removeLocked(c)
	}

	delete(t.nodes, id)
}

// Node returns a copy of the node with handle id.
func (t *Tree) Node(id uint64) (Node, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	n, ok := t.nodes[id]
	if !ok {
		return Node{}, false
	}

	cp := *n
	cp.Children = append([]uint64(nil), n.Children...)

	return cp, true
}

// Children returns the child handles of id.
func (t *Tree) Children(id uint64) []uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	n, ok := t.nodes[id]
	if !ok {
		return nil
	}

	return append([]uint64(nil), n.Children...)
}

// Path returns the titles from the top-level ancestor down to id.
func (t *Tree) Path(id uint64) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var path []string

	for n, ok := t.nodes[id]; ok && n.ID != RootID; n, ok = t.nodes[n.Parent] {
		path = append([]string{n.Title}, path...)
	}

	return path
}

// Len returns the number of nodes, excluding the root.
func (t *Tree) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.nodes) - 1
}
