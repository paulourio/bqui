//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

// Menu is a top-level menu of the main menu bar.
type Menu struct {
	Title string
	Items []MenuItem
	Role  MenuRole
}

// MenuRole marks menus AppKit manages specially.
type MenuRole int

// Menu roles.
const (
	MenuPlain MenuRole = iota
	MenuApp
	MenuWindows
	MenuHelp
)

// MenuItem describes one menu item. A zero MenuItem is a separator.
//
// When Run is set the item targets a Go callback; otherwise Action is sent
// through the responder chain (e.g. "copy:", "toggleSidebar:").
type MenuItem struct {
	Title   string
	Action  string
	Run     func()
	Key     string // key equivalent; "" for none
	Mods    uint64 // modifier mask; 0 means Command only (AppKit default)
	Tag     int64
	Submenu *Menu
}

func (it MenuItem) separator() bool {
	return it.Title == "" && it.Action == "" && it.Run == nil && it.Submenu == nil
}

// SetMainMenu builds and installs the main menu bar.
func SetMainMenu(app objc.ID, menus []Menu) {
	bar := newMenu("")

	for i := range menus {
		m := &menus[i]
		item := objc.New("NSMenuItem")
		sub := buildMenu(m)
		objc.Msg(item, "setSubmenu:", sub)
		objc.Msg(bar, "addItem:", item)

		switch m.Role {
		case MenuWindows:
			objc.Msg(app, "setWindowsMenu:", sub)
		case MenuHelp:
			objc.Msg(app, "setHelpMenu:", sub)
		case MenuPlain, MenuApp:
		}
	}

	objc.Msg(app, "setMainMenu:", bar)
}

func newMenu(title string) objc.ID {
	return objc.Msg(objc.Msg(objc.Cls("NSMenu"), "alloc"), "initWithTitle:", objc.String(title))
}

func buildMenu(m *Menu) objc.ID {
	menu := newMenu(m.Title)

	for _, it := range m.Items {
		if it.separator() {
			objc.Msg(menu, "addItem:", objc.Msg(objc.Cls("NSMenuItem"), "separatorItem"))

			continue
		}

		objc.Msg(menu, "addItem:", buildItem(it))
	}

	return menu
}

func buildItem(it MenuItem) objc.ID {
	action := objc.SEL(0)
	if it.Action != "" {
		action = objc.Sel(it.Action)
	}

	if it.Run != nil {
		action = objc.Sel(objc.ActionSelector)
	}

	mi := objc.Msg(objc.Msg(objc.Cls("NSMenuItem"), "alloc"),
		"initWithTitle:action:keyEquivalent:",
		objc.String(it.Title), action, objc.String(it.Key))

	if it.Run != nil {
		run := it.Run
		objc.Msg(mi, "setTarget:", objc.NewTarget(func(objc.ID) { run() }))
	}

	if it.Mods != 0 {
		objc.Msg(mi, "setKeyEquivalentModifierMask:", it.Mods)
	}

	if it.Tag != 0 {
		objc.Msg(mi, "setTag:", it.Tag)
	}

	if it.Submenu != nil {
		objc.Msg(mi, "setSubmenu:", buildMenu(it.Submenu))
	}

	return mi
}
