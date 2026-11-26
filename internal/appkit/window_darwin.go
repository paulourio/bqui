//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

// NSWindowStyleMask values.
const (
	WindowStyleTitled              uint64 = 1 << 0
	WindowStyleClosable            uint64 = 1 << 1
	WindowStyleMiniaturizable      uint64 = 1 << 2
	WindowStyleResizable           uint64 = 1 << 3
	WindowStyleFullSizeContentView uint64 = 1 << 15
)

const (
	backingStoreBuffered      uint64 = 2
	windowToolbarStyleUnified int64  = 3
	toolbarDisplayIconOnly    uint64 = 2
)

// NewWindow creates a titled, resizable window with a full-size content view,
// the standard layout for sidebar apps on macOS 26.
func NewWindow(title string, frame Rect) objc.ID {
	style := WindowStyleTitled | WindowStyleClosable | WindowStyleMiniaturizable |
		WindowStyleResizable | WindowStyleFullSizeContentView

	w := objc.Msg(objc.Msg(objc.Cls("NSWindow"), "alloc"),
		"initWithContentRect:styleMask:backing:defer:", frame, style, backingStoreBuffered, false)
	objc.Msg(w, "setTitle:", objc.String(title))
	objc.Msg(w, "setReleasedWhenClosed:", false)

	return w
}

// ToolbarItemKind selects how a toolbar item is built.
type ToolbarItemKind int

// Toolbar item kinds.
const (
	ToolbarButton ToolbarItemKind = iota
	ToolbarCustomView
	ToolbarSearch
	ToolbarFlexibleSpace
	ToolbarToggleSidebar
	ToolbarSidebarSeparator
	ToolbarToggleInspector
	ToolbarInspectorSeparator
)

// ToolbarItem describes one toolbar item.
type ToolbarItem struct {
	ID      string // required for Button/CustomView/Search
	Kind    ToolbarItemKind
	Label   string
	Symbol  string // SF Symbol name for buttons
	ToolTip string
	View    objc.ID // for ToolbarCustomView
	Run     func()

	item objc.ID
}

type toolbarState struct {
	ids   []objc.ID
	items map[string]*ToolbarItem
}

var (
	toolbars          objc.Registry[*toolbarState]
	toolbarDelegateCl = objc.LazyClass("BQUIToolbarDelegate", "NSObject", []string{"NSToolbarDelegate"}, func() []objc.Method {
		identifiers := func(self objc.ID, _ objc.SEL, _ objc.ID) objc.ID {
			st, ok := toolbars.Get(self)
			if !ok {
				return objc.Array()
			}

			return objc.Array(st.ids...)
		}

		return []objc.Method{
			{Sel: "toolbarDefaultItemIdentifiers:", Fn: identifiers},
			{Sel: "toolbarAllowedItemIdentifiers:", Fn: identifiers},
			{
				Sel: "toolbar:itemForItemIdentifier:willBeInsertedIntoToolbar:",
				Fn: func(self objc.ID, _ objc.SEL, _, ident objc.ID, _ bool) objc.ID {
					st, ok := toolbars.Get(self)
					if !ok {
						return objc.Nil
					}

					if it, ok := st.items[objc.GoString(ident)]; ok {
						return it.item
					}

					return objc.Nil
				},
			},
		}
	})
)

// SetToolbar installs a unified, icon-only toolbar on window.
func SetToolbar(window objc.ID, identifier string, items []*ToolbarItem) {
	st := &toolbarState{items: make(map[string]*ToolbarItem)}

	for _, it := range items {
		ident := toolbarIdentifier(it)
		st.ids = append(st.ids, ident)

		if it.item = buildToolbarItem(it, ident); it.item != objc.Nil {
			st.items[it.ID] = it
		}
	}

	del := objc.Msg(objc.Msg(toolbarDelegateCl(), "alloc"), "init")
	toolbars.Put(del, st)

	tb := objc.Msg(objc.Msg(objc.Cls("NSToolbar"), "alloc"), "initWithIdentifier:", objc.String(identifier))
	objc.Msg(tb, "setDelegate:", del)
	objc.Msg(tb, "setDisplayMode:", toolbarDisplayIconOnly)
	objc.Msg(tb, "setAllowsUserCustomization:", false)
	objc.Msg(window, "setToolbar:", tb)
	objc.Msg(window, "setToolbarStyle:", windowToolbarStyleUnified)
}

func toolbarIdentifier(it *ToolbarItem) objc.ID {
	switch it.Kind {
	case ToolbarFlexibleSpace:
		return constant("NSToolbarFlexibleSpaceItemIdentifier", "NSToolbarFlexibleSpaceItem")
	case ToolbarToggleSidebar:
		return constant("NSToolbarToggleSidebarItemIdentifier", "NSToolbarToggleSidebarItem")
	case ToolbarSidebarSeparator:
		return constant("NSToolbarSidebarTrackingSeparatorItemIdentifier", "NSToolbarSidebarTrackingSeparatorItemIdentifier")
	case ToolbarToggleInspector:
		return constant("NSToolbarToggleInspectorItemIdentifier", "NSToolbarToggleInspectorItem")
	case ToolbarInspectorSeparator:
		return constant("NSToolbarInspectorTrackingSeparatorItemIdentifier", "NSToolbarInspectorTrackingSeparatorItemIdentifier")
	case ToolbarButton, ToolbarCustomView, ToolbarSearch:
	}

	return objc.Retain(objc.String(it.ID))
}

func buildToolbarItem(it *ToolbarItem, ident objc.ID) objc.ID {
	var item objc.ID

	switch it.Kind {
	case ToolbarButton:
		item = objc.Msg(objc.Msg(objc.Cls("NSToolbarItem"), "alloc"), "initWithItemIdentifier:", ident)
		objc.Msg(item, "setBordered:", true)

		if it.Symbol != "" {
			objc.Msg(item, "setImage:", SymbolImage(it.Symbol, it.Label))
		}
	case ToolbarCustomView:
		item = objc.Msg(objc.Msg(objc.Cls("NSToolbarItem"), "alloc"), "initWithItemIdentifier:", ident)
		objc.Msg(item, "setView:", it.View)
	case ToolbarSearch:
		item = objc.Msg(objc.Msg(objc.Cls("NSSearchToolbarItem"), "alloc"), "initWithItemIdentifier:", ident)
	default:
		return objc.Nil // system items are created by AppKit
	}

	objc.Msg(item, "setLabel:", objc.String(it.Label))
	objc.Msg(item, "setPaletteLabel:", objc.String(it.Label))

	if it.ToolTip != "" {
		objc.Msg(item, "setToolTip:", objc.String(it.ToolTip))
	}

	if it.Run != nil {
		run := it.Run
		objc.Msg(item, "setTarget:", objc.NewTarget(func(objc.ID) { run() }))
		objc.Msg(item, "setAction:", objc.Sel(objc.ActionSelector))
	}

	return item
}
