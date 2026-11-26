//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

// ViewController wraps view in a plain NSViewController.
func ViewController(view objc.ID) objc.ID {
	vc := objc.New("NSViewController")
	objc.Msg(vc, "setView:", view)

	return vc
}

// SplitItemKind selects the NSSplitViewItem behavior.
type SplitItemKind int

// Split item kinds.
const (
	SplitContent   SplitItemKind = iota // plain item
	SplitSidebar                        // source-list sidebar (Liquid Glass on macOS 26)
	SplitInspector                      // trailing inspector (macOS 14+)
)

// SplitItem describes one pane of a split view controller.
type SplitItem struct {
	Kind          SplitItemKind
	Controller    objc.ID
	MinThickness  float64
	MaxThickness  float64
	Collapsed     bool
	HoldingWeight float32 // NSLayoutPriority; lower panes give space first
}

// SplitViewController builds an NSSplitViewController. vertical=true lays the
// panes side by side.
func SplitViewController(vertical bool, autosave string, items []SplitItem) objc.ID {
	svc := objc.New("NSSplitViewController")
	sv := objc.Msg(svc, "splitView")
	objc.Msg(sv, "setVertical:", vertical)

	if autosave != "" {
		objc.Msg(sv, "setAutosaveName:", objc.String(autosave))
	}

	for _, it := range items {
		ctor := "splitViewItemWithViewController:"

		switch it.Kind {
		case SplitSidebar:
			ctor = "sidebarWithViewController:"
		case SplitInspector:
			ctor = "inspectorWithViewController:"
		case SplitContent:
		}

		item := objc.Msg(objc.Cls("NSSplitViewItem"), ctor, it.Controller)

		if it.MinThickness > 0 {
			objc.Msg(item, "setMinimumThickness:", it.MinThickness)
		}

		if it.MaxThickness > 0 {
			objc.Msg(item, "setMaximumThickness:", it.MaxThickness)
		}

		if it.HoldingWeight > 0 {
			objc.Msg(item, "setHoldingPriority:", it.HoldingWeight)
		}

		objc.Msg(item, "setCollapsed:", it.Collapsed)
		objc.Msg(svc, "addSplitViewItem:", item)
	}

	return svc
}
