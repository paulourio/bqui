package app

import (
	"slices"
	"sync"
	"testing"
)

func TestTreeAddChildrenPathClear(t *testing.T) {
	tr := NewTree()
	p := tr.Add(RootID, KindProject, "proj", "")
	d := tr.Add(p, KindDataset, "ds", "")
	tb := tr.Add(d, KindTable, "events", "partitioned")
	tr.Add(d, KindView, "v_events", "")

	if got := tr.Children(d); len(got) != 2 || got[0] != tb {
		t.Fatalf("Children(ds) = %v", got)
	}

	if got := tr.Path(tb); !slices.Equal(got, []string{"proj", "ds", "events"}) {
		t.Fatalf("Path = %v", got)
	}

	if n, ok := tr.Node(tb); !ok || n.Kind != KindTable || n.Detail != "partitioned" || n.Parent != d {
		t.Fatalf("Node = %+v, %v", n, ok)
	}

	if tr.Add(9999, KindTable, "orphan", "") != 0 {
		t.Fatal("Add under missing parent should return 0")
	}

	tr.Clear(p)

	if tr.Len() != 1 || len(tr.Children(p)) != 0 {
		t.Fatalf("after Clear: len=%d children=%v", tr.Len(), tr.Children(p))
	}

	if _, ok := tr.Node(tb); ok {
		t.Fatal("grandchild survived Clear")
	}
}

func TestTreeNodeReturnsCopy(t *testing.T) {
	tr := NewTree()
	p := tr.Add(RootID, KindProject, "proj", "")
	tr.Add(p, KindDataset, "a", "")

	n, _ := tr.Node(p)
	n.Children[0] = 42

	if got, _ := tr.Node(p); got.Children[0] == 42 {
		t.Fatalf("Node leaked internal state: %+v", got)
	}
}

func TestTreeConcurrentUse(t *testing.T) {
	tr := NewTree()
	p := tr.Add(RootID, KindProject, "proj", "")

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 100 {
				tr.Add(p, KindDataset, "d", "")
				_ = tr.Children(p)
			}
		})
	}

	wg.Wait()

	if n := len(tr.Children(p)); n != 800 {
		t.Fatalf("children = %d, want 800", n)
	}
}

func TestNodeKindContainer(t *testing.T) {
	for _, k := range []NodeKind{KindRoot, KindSection, KindProject, KindDataset} {
		if !k.Container() {
			t.Errorf("%v should be a container", k)
		}
	}

	for _, k := range []NodeKind{KindTable, KindView, KindRoutine, KindModel, KindPlaceholder} {
		if k.Container() {
			t.Errorf("%v should not be a container", k)
		}
	}

	if NodeKind(99).String() != "unknown" {
		t.Error("out-of-range kind name")
	}
}
