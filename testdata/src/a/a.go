package a

import "sync"

type Alias = sync.Map

type NotMap sync.Map

type Holder struct {
	M sync.Map
	N int
}

type Embedded struct {
	sync.Map
}

func unusedAssignment() {
	var m sync.Map
	n := m
	_ = n
}

func copyBeforeUse() {
	var m sync.Map
	n := m
	m.Store("x", 1)
	_ = n
}

func unusedReturn() sync.Map {
	var m sync.Map
	return m
}

func usedAssignment() {
	var m sync.Map
	m.Store("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func loadThenCopy() {
	var m sync.Map
	m.Load("x")
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func pointerAlias() {
	var m sync.Map
	p := &m
	p.Store("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func use(m *sync.Map) {
	m.Store("x", 1)
}

func interprocedural() {
	var m sync.Map
	use(&m)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func usedReturn() sync.Map {
	var m sync.Map
	m.Store("x", 1)
	return m // want "syncmapcopy: sync.Map is copied after first use"
}

func take(sync.Map) {}

func passedByValue() {
	var m sync.Map
	m.Store("x", 1)
	take(m) // want "syncmapcopy: sync.Map is copied after first use"
}

func compositeCopies() {
	var m sync.Map
	m.Store("x", 1)
	_ = Holder{M: m}   // want "syncmapcopy: sync.Map is copied after first use"
	_ = []sync.Map{m}  // want "syncmapcopy: sync.Map is copied after first use"
	_ = [1]sync.Map{m} // want "syncmapcopy: sync.Map is copied after first use"
}

func aggregateAssignment() {
	var src Holder
	src.M.Store("x", 1)
	var dst Holder
	dst = src // want "syncmapcopy: sync.Map is copied after first use"
	_ = dst
}

func aggregateCopyBeforeUse() {
	var src Holder
	dst := src
	src.M.Store("x", 1)
	_ = dst
}

func directAssignment() {
	var src sync.Map
	var dst sync.Map
	src.Store("x", 1)
	dst = src // want "syncmapcopy: sync.Map is copied after first use"
	_ = dst
}

func aliasType() {
	var m Alias
	m.Store("x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func unrelatedType() {
	var m NotMap
	n := m
	_ = n
}

func opaque(*sync.Map) {}

func unknownIsNotReported() {
	var m sync.Map
	var functionValue func(*sync.Map)
	functionValue(&m)
	n := m
	_ = n
}

func usedSurvivesOpaqueCall() {
	var m sync.Map
	m.Store("x", 1)
	var functionValue func(*sync.Map)
	functionValue(&m)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func embeddedMap() {
	var value Embedded
	value.Store("x", 1)
	copy := value // want "syncmapcopy: sync.Map is copied after first use"
	_ = copy
}

func methodExpression() {
	var m sync.Map
	(*sync.Map).Store(&m, "x", 1)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func deferredUseOccursAfterCopy() {
	var m sync.Map
	defer m.Clear()
	n := m
	_ = n
}

func deferredUse(m *sync.Map) {
	defer m.Store("x", 1)
}

func deferredInterprocedural() {
	var m sync.Map
	deferredUse(&m)
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}

func closureCopy() {
	func() {
		var m sync.Map
		m.Store("x", 1)
		n := m // want "syncmapcopy: sync.Map is copied after first use"
		_ = n
	}()
}

func branchIsNotDefinitelyUsed(condition bool) {
	var m sync.Map
	if condition {
		m.Store("x", 1)
	}
	n := m
	_ = n
}

func bothBranchesUse(condition bool) {
	var m sync.Map
	if condition {
		m.Store("x", 1)
	} else {
		m.Load("x")
	}
	n := m // want "syncmapcopy: sync.Map is copied after first use"
	_ = n
}
