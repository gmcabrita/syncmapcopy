package methods

import "sync"

func load() {
	var m sync.Map
	m.Load("x")
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func store() {
	var m sync.Map
	m.Store("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func loadOrStore() {
	var m sync.Map
	m.LoadOrStore("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func loadAndDelete() {
	var m sync.Map
	m.LoadAndDelete("x")
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func deleteValue() {
	var m sync.Map
	m.Delete("x")
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func swap() {
	var m sync.Map
	m.Swap("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func compareAndSwap() {
	var m sync.Map
	m.CompareAndSwap("x", 1, 2)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func compareAndDelete() {
	var m sync.Map
	m.CompareAndDelete("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func rangeMap() {
	var m sync.Map
	m.Range(func(any, any) bool { return true })
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func clearMap() {
	var m sync.Map
	m.Clear()
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}
