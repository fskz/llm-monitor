package tui

import (
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// overviewPane is the left column: one entry per provider with its monitor
// badge. Selection state survives refreshes by provider id, not index.
type overviewPane struct {
	st  *store.Store
	eng view.EngineStatus

	list *tview.List
	// ids[i] is the provider id of list item i (refresh order = store order).
	ids []int
	// selected is preserved across redraws; -1 when a deleted provider was
	// selected (fall back to the first entry).
	selected int
	// onChange fires with the newly selected provider id.
	onChange func(id int)
}

func newOverviewPane(st *store.Store, eng view.EngineStatus, onChange func(int)) *overviewPane {
	return &overviewPane{st: st, eng: eng, list: tview.NewList(), selected: -1, onChange: onChange}
}

// refresh rebuilds the list from the store, restoring the selection by id.
func (o *overviewPane) refresh() {
	providers := o.st.GetProviders()

	if o.selected == -1 && len(providers) > 0 {
		o.selected = providers[0].ID
	}
	// Keep selection only while the provider still exists.
	if o.selected != -1 {
		found := false
		for _, p := range providers {
			if p.ID == o.selected {
				found = true
				break
			}
		}
		if !found {
			if len(providers) > 0 {
				o.selected = providers[0].ID
			} else {
				o.selected = -1
			}
		}
	}

	o.list.Clear()
	o.ids = o.ids[:0]
	for _, p := range providers {
		v := view.ViewOf(o.st, o.eng, p)
		// tview List has no per-item color API; the badge is colored through
		// an inline color tag instead (name stays default-colored).
		item := p.Name + "  [" + monitorColor(v.Status) + "::b]" + view.MonitorText(v.Status) + "[-]"
		secondary := ""
		if v.Probing {
			secondary = "探测中…"
		}
		o.list.AddItem(item, secondary, 0, nil)
		o.ids = append(o.ids, p.ID)
	}

	// Restore cursor position by id.
	for i, id := range o.ids {
		if id == o.selected {
			o.list.SetCurrentItem(i)
			break
		}
	}
	if len(providers) == 0 {
		o.list.AddItem("[gray]暂无监测对象（阶段 4b 支持新建）[-]", "", 0, nil)
	}
}

// selectedID returns the currently selected provider id (-1 = none).
func (o *overviewPane) selectedID() int { return o.selected }

// wireSelection bridges tview list selection events to o.selected/onChange.
func (o *overviewPane) wireSelection() {
	o.list.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(o.ids) {
			return
		}
		o.selected = o.ids[index]
		if o.onChange != nil {
			o.onChange(o.selected)
		}
	})
}
