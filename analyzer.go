package syncmapcopy

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strconv"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/cfg"
)

const diagnostic = "syncmapcopy: sync.Map is copied after first use"

var mapMethods = map[string]struct{}{
	"Load":             {},
	"Store":            {},
	"LoadOrStore":      {},
	"LoadAndDelete":    {},
	"Delete":           {},
	"Swap":             {},
	"CompareAndSwap":   {},
	"CompareAndDelete": {},
	"Range":            {},
	"Clear":            {},
}

// Analyzer reports copies of sync.Map values that occur after an API method
// has been called on that value.
var Analyzer = &analysis.Analyzer{
	Name: "syncmapcopy",
	Doc:  "report copies of sync.Map values after their first use",
	Run:  run,
}

type useState uint8

const (
	unknown useState = iota
	unused
	used
)

type mapStatus struct {
	state useState
	first token.Pos
	end   token.Pos
}

type slot struct {
	object types.Object
	path   string
}

type pointer struct {
	target slot
	known  bool
}

type flowState struct {
	maps     map[slot]mapStatus
	ptrs     map[types.Object]pointer
	deferred map[slot]deferredStatus
}

type effect uint8

const (
	unchanged effect = iota
	makesUsed
	makesUnknown
)

type deferredStatus struct {
	effect effect
	first  token.Pos
	end    token.Pos
}

type function struct {
	body   *ast.BlockStmt
	object *types.Func
	sig    *types.Signature
}

type checker struct {
	pass      *analysis.Pass
	functions []*function
	summaries map[*types.Func][]effect
	reported  map[token.Pos]bool
}

type functionAnalyzer struct {
	checker     *checker
	fn          *function
	report      bool
	summaryMode bool
	paramSlots  map[int]slot
}

type value struct {
	states      map[string]mapStatus
	constructed bool
}

func run(pass *analysis.Pass) (any, error) {
	c := &checker{
		pass:      pass,
		summaries: make(map[*types.Func][]effect),
		reported:  make(map[token.Pos]bool),
	}
	c.collectFunctions()
	c.computeSummaries()

	for _, fn := range c.functions {
		analyzer := functionAnalyzer{checker: c, fn: fn, report: true}
		analyzer.analyze()
	}
	return nil, nil
}

func (c *checker) collectFunctions() {
	for _, file := range c.pass.Files {
		for _, declaration := range file.Decls {
			decl, ok := declaration.(*ast.FuncDecl)
			if !ok || decl.Body == nil {
				continue
			}
			object, _ := c.pass.TypesInfo.Defs[decl.Name].(*types.Func)
			if object == nil {
				continue
			}
			sig, _ := object.Type().(*types.Signature)
			if sig == nil {
				continue
			}
			c.functions = append(c.functions, &function{body: decl.Body, object: object, sig: sig})
			ast.Inspect(decl.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.FuncLit)
				if !ok {
					return true
				}
				sig, _ := c.pass.TypesInfo.TypeOf(literal).(*types.Signature)
				if sig != nil {
					c.functions = append(c.functions, &function{body: literal.Body, sig: sig})
				}
				return true
			})
		}
	}
}

func (c *checker) computeSummaries() {
	for _, fn := range c.functions {
		if fn.object != nil {
			c.summaries[fn.object] = make([]effect, fn.sig.Params().Len())
		}
	}

	for range len(c.functions) + 1 {
		changed := false
		for _, fn := range c.functions {
			if fn.object == nil {
				continue
			}
			analyzer := functionAnalyzer{
				checker:     c,
				fn:          fn,
				summaryMode: true,
				paramSlots:  make(map[int]slot),
			}
			exit := analyzer.analyze()
			next := make([]effect, fn.sig.Params().Len())
			for index, target := range analyzer.paramSlots {
				switch exit.maps[target].state {
				case unused:
					next[index] = unchanged
				case used:
					next[index] = makesUsed
				default:
					next[index] = makesUnknown
				}
			}
			if !slices.Equal(next, c.summaries[fn.object]) {
				c.summaries[fn.object] = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
}

func (a *functionAnalyzer) analyze() flowState {
	graph := cfg.New(a.fn.body, func(*ast.CallExpr) bool { return true })
	inputs := make([]*flowState, len(graph.Blocks))
	entry := a.entryState()
	inputs[0] = &entry
	queue := []int{0}
	queued := make([]bool, len(graph.Blocks))
	queued[0] = true
	var exit *flowState

	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
		queued[index] = false
		block := graph.Blocks[index]
		state := inputs[index].clone()
		for _, node := range block.Nodes {
			a.processNode(node, &state)
		}
		if block.Return() != nil {
			if exit == nil {
				copy := state.clone()
				exit = &copy
			} else {
				exit.join(state)
			}
		}
		for _, successor := range block.Succs {
			i := int(successor.Index)
			if inputs[i] == nil {
				copy := state.clone()
				inputs[i] = &copy
				if !queued[i] {
					queue = append(queue, i)
					queued[i] = true
				}
				continue
			}
			if inputs[i].join(state) && !queued[i] {
				queue = append(queue, i)
				queued[i] = true
			}
		}
	}

	if exit == nil {
		return entry
	}
	return *exit
}

func (a *functionAnalyzer) entryState() flowState {
	state := flowState{
		maps:     make(map[slot]mapStatus),
		ptrs:     make(map[types.Object]pointer),
		deferred: make(map[slot]deferredStatus),
	}
	sig := a.fn.sig
	if sig.Recv() != nil {
		a.initializeParameter(sig.Recv(), -1, &state)
	}
	for i := range sig.Params().Len() {
		a.initializeParameter(sig.Params().At(i), i, &state)
	}
	for i := range sig.Results().Len() {
		result := sig.Results().At(i)
		if result.Name() != "" {
			initializeObject(result, unused, &state)
		}
	}
	return state
}

func (a *functionAnalyzer) initializeParameter(object *types.Var, index int, state *flowState) {
	if isSyncMapPointer(object.Type()) {
		target := slot{object: object, path: "*"}
		initial := unknown
		if a.summaryMode {
			initial = unused
			if index >= 0 {
				a.paramSlots[index] = target
			}
		}
		state.maps[target] = mapStatus{state: initial}
		state.ptrs[object] = pointer{target: target, known: true}
		return
	}
	initializeObject(object, unknown, state)
}

func (a *functionAnalyzer) processNode(node ast.Node, state *flowState) {
	switch node := node.(type) {
	case *ast.AssignStmt:
		a.processAssignment(node, state)
	case *ast.ValueSpec:
		a.processValueSpec(node, state)
	case *ast.ReturnStmt:
		for _, result := range node.Results {
			a.copyExpr(result, state)
		}
		a.applyDeferred(state)
	case *ast.ExprStmt:
		a.evalExpr(node.X, state)
	case *ast.GoStmt:
		a.evalCall(node.Call, state)
	case *ast.DeferStmt:
		a.evalDeferredCall(node.Call, state)
	case *ast.SendStmt:
		a.evalExpr(node.Chan, state)
		a.copyExpr(node.Value, state)
	case ast.Expr:
		a.evalExpr(node, state)
	}
}

func (a *functionAnalyzer) processValueSpec(spec *ast.ValueSpec, state *flowState) {
	values := make([]value, len(spec.Values))
	for i, expression := range spec.Values {
		values[i] = a.copyExpr(expression, state)
	}
	for _, name := range spec.Names {
		if object := a.checker.pass.TypesInfo.Defs[name]; object != nil {
			initializeObject(object, unused, state)
		}
	}
	for i, expressionValue := range values {
		if i >= len(spec.Names) {
			break
		}
		a.assignValue(spec.Names[i], expressionValue, state)
		a.assignPointer(spec.Names[i], spec.Values[i], state)
	}
}

func (a *functionAnalyzer) processAssignment(assign *ast.AssignStmt, state *flowState) {
	values := make([]value, len(assign.Rhs))
	for i, expression := range assign.Rhs {
		if i < len(assign.Lhs) && isBlank(assign.Lhs[i]) {
			a.evalExpr(expression, state)
			continue
		}
		values[i] = a.copyExpr(expression, state)
	}
	if assign.Tok == token.DEFINE {
		for _, expression := range assign.Lhs {
			identifier, ok := expression.(*ast.Ident)
			if !ok {
				continue
			}
			if object := a.checker.pass.TypesInfo.Defs[identifier]; object != nil {
				initializeObject(object, unused, state)
			}
		}
	}
	for i, expressionValue := range values {
		if i >= len(assign.Lhs) || isBlank(assign.Lhs[i]) {
			continue
		}
		a.assignValue(assign.Lhs[i], expressionValue, state)
		a.assignPointer(assign.Lhs[i], assign.Rhs[i], state)
	}
}

func (a *functionAnalyzer) copyExpr(expression ast.Expr, state *flowState) value {
	expression = ast.Unparen(expression)
	if literal, ok := expression.(*ast.CompositeLit); ok {
		return a.compositeValue(literal, state)
	}
	if call, ok := expression.(*ast.CallExpr); ok {
		a.evalCall(call, state)
		return unknownValue(a.checker.pass.TypesInfo.TypeOf(expression))
	}

	a.evalExpr(expression, state)
	result := a.valueOf(expression, state)
	if len(result.states) == 0 {
		return result
	}
	for _, status := range result.states {
		if status.state == used {
			a.reportCopy(expression, status)
			break
		}
	}
	return result
}

func (a *functionAnalyzer) compositeValue(literal *ast.CompositeLit, state *flowState) value {
	typ := a.checker.pass.TypesInfo.TypeOf(literal)
	result := zeroValue(typ)
	result.constructed = true
	underlying := types.Unalias(typ)
	if named, ok := underlying.(*types.Named); ok {
		underlying = named.Underlying()
	}

	for index, element := range literal.Elts {
		valueExpr := element
		var key ast.Expr
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			key, valueExpr = pair.Key, pair.Value
		}
		child := a.copyExpr(valueExpr, state)
		prefix, ok := compositePrefix(a.checker.pass.TypesInfo, underlying, key, index)
		if !ok {
			continue
		}
		for path, status := range child.states {
			result.states[prefix+path] = status
		}
	}
	return result
}

func compositePrefix(info *types.Info, typ types.Type, key ast.Expr, index int) (string, bool) {
	switch typ := typ.(type) {
	case *types.Struct:
		fieldIndex := index
		if key != nil {
			identifier, ok := key.(*ast.Ident)
			if !ok {
				return "", false
			}
			field, _ := info.Uses[identifier].(*types.Var)
			fieldIndex = -1
			for i := range typ.NumFields() {
				if typ.Field(i) == field {
					fieldIndex = i
					break
				}
			}
			if fieldIndex < 0 {
				return "", false
			}
		}
		return fieldPath(fieldIndex), true
	case *types.Array:
		if key != nil {
			constantIndex, ok := constantInt(info, key)
			if !ok {
				return "", false
			}
			index = constantIndex
		}
		return arrayPath(index), true
	case *types.Slice, *types.Map:
		return "", false
	default:
		return "", true
	}
}

func (a *functionAnalyzer) valueOf(expression ast.Expr, state *flowState) value {
	typ := a.checker.pass.TypesInfo.TypeOf(expression)
	paths := mapPaths(typ)
	result := value{states: make(map[string]mapStatus, len(paths))}
	location, located := a.location(expression, state)
	for _, path := range paths {
		status := mapStatus{state: unknown}
		if located {
			status = state.maps[slot{object: location.object, path: location.path + path}]
			if status.state == 0 {
				status.state = unknown
			}
		}
		result.states[path] = status
	}
	return result
}

func (a *functionAnalyzer) assignValue(destination ast.Expr, source value, state *flowState) {
	location, ok := a.location(destination, state)
	if !ok {
		return
	}
	for _, path := range mapPaths(a.checker.pass.TypesInfo.TypeOf(destination)) {
		status, ok := source.states[path]
		if !ok {
			status = mapStatus{state: unknown}
		}
		state.maps[slot{object: location.object, path: location.path + path}] = status
	}
}

func (a *functionAnalyzer) assignPointer(destination, source ast.Expr, state *flowState) {
	object := assignedObject(a.checker.pass.TypesInfo, destination)
	if object == nil || !isSyncMapPointer(object.Type()) {
		return
	}
	target, known := a.pointerTarget(source, state)
	state.ptrs[object] = pointer{target: target, known: known}
}

func (a *functionAnalyzer) evalExpr(expression ast.Expr, state *flowState) {
	if expression == nil {
		return
	}
	switch expression := ast.Unparen(expression).(type) {
	case *ast.CallExpr:
		a.evalCall(expression, state)
	case *ast.BinaryExpr:
		a.evalExpr(expression.X, state)
		a.evalExpr(expression.Y, state)
	case *ast.UnaryExpr:
		a.evalExpr(expression.X, state)
	case *ast.SelectorExpr:
		a.evalExpr(expression.X, state)
	case *ast.IndexExpr:
		a.evalExpr(expression.X, state)
		a.evalExpr(expression.Index, state)
	case *ast.IndexListExpr:
		a.evalExpr(expression.X, state)
	case *ast.SliceExpr:
		a.evalExpr(expression.X, state)
		a.evalExpr(expression.Low, state)
		a.evalExpr(expression.High, state)
		a.evalExpr(expression.Max, state)
	case *ast.TypeAssertExpr:
		a.evalExpr(expression.X, state)
	case *ast.StarExpr:
		a.evalExpr(expression.X, state)
	case *ast.CompositeLit:
		a.compositeValue(expression, state)
	}
}

func (a *functionAnalyzer) evalCall(call *ast.CallExpr, state *flowState) {
	if receiver, ok := a.syncMapReceiver(call); ok {
		a.evalExpr(receiver, state)
		start := 0
		if selection := selectionOf(a.checker.pass.TypesInfo, call.Fun); selection != nil && selection.Kind() == types.MethodExpr {
			start = 1
		}
		for _, argument := range call.Args[start:] {
			a.copyExpr(argument, state)
		}
		target, known := a.syncMapTarget(call, receiver, state)
		if known {
			a.markUsed(target, call.Fun.Pos(), call.Fun.End(), state)
		}
		return
	}

	if a.isNonCopyingBuiltin(call) {
		for _, argument := range call.Args {
			a.evalExpr(argument, state)
		}
		return
	}

	if selection := selectionOf(a.checker.pass.TypesInfo, call.Fun); selection != nil && selection.Kind() == types.MethodVal {
		receiver := ast.Unparen(call.Fun).(*ast.SelectorExpr).X
		if !isPointer(selection.Recv()) {
			a.copyExpr(receiver, state)
		} else {
			a.evalExpr(receiver, state)
		}
	} else {
		a.evalExpr(call.Fun, state)
	}

	for _, argument := range call.Args {
		if len(mapPaths(a.checker.pass.TypesInfo.TypeOf(argument))) > 0 {
			a.copyExpr(argument, state)
		} else if literal, ok := ast.Unparen(argument).(*ast.CompositeLit); ok {
			a.compositeValue(literal, state)
		} else {
			a.evalExpr(argument, state)
		}
	}

	callee := calledFunction(a.checker.pass.TypesInfo, call.Fun)
	effects, local := a.checker.summaries[callee]
	signature := callSignature(a.checker.pass.TypesInfo, call.Fun)
	if signature == nil {
		return
	}
	for i, argument := range call.Args {
		parameter := parameterAt(signature, i)
		if parameter == nil || !isSyncMapPointer(parameter.Type()) {
			continue
		}
		target, known := a.pointerTarget(argument, state)
		if !known {
			continue
		}
		if !local || i >= len(effects) {
			a.markUnknown(target, state)
			continue
		}
		switch effects[i] {
		case makesUsed:
			a.markUsed(target, call.Fun.Pos(), call.Fun.End(), state)
		case makesUnknown:
			a.markUnknown(target, state)
		}
	}
}

func (a *functionAnalyzer) evalDeferredCall(call *ast.CallExpr, state *flowState) {
	if receiver, ok := a.syncMapReceiver(call); ok {
		a.evalExpr(receiver, state)
		start := 0
		if selection := selectionOf(a.checker.pass.TypesInfo, call.Fun); selection != nil && selection.Kind() == types.MethodExpr {
			start = 1
		}
		for _, argument := range call.Args[start:] {
			a.copyExpr(argument, state)
		}
		if target, known := a.syncMapTarget(call, receiver, state); known {
			a.scheduleDeferred(target, makesUsed, call.Fun.Pos(), call.Fun.End(), state)
		}
		return
	}

	if selection := selectionOf(a.checker.pass.TypesInfo, call.Fun); selection != nil && selection.Kind() == types.MethodVal {
		receiver := ast.Unparen(call.Fun).(*ast.SelectorExpr).X
		if !isPointer(selection.Recv()) {
			a.copyExpr(receiver, state)
		} else {
			a.evalExpr(receiver, state)
		}
	} else {
		a.evalExpr(call.Fun, state)
	}
	for _, argument := range call.Args {
		if len(mapPaths(a.checker.pass.TypesInfo.TypeOf(argument))) > 0 {
			a.copyExpr(argument, state)
		} else {
			a.evalExpr(argument, state)
		}
	}

	callee := calledFunction(a.checker.pass.TypesInfo, call.Fun)
	effects, local := a.checker.summaries[callee]
	signature := callSignature(a.checker.pass.TypesInfo, call.Fun)
	if signature == nil {
		return
	}
	for i, argument := range call.Args {
		parameter := parameterAt(signature, i)
		if parameter == nil || !isSyncMapPointer(parameter.Type()) {
			continue
		}
		target, known := a.pointerTarget(argument, state)
		if !known {
			continue
		}
		deferredEffect := makesUnknown
		if local && i < len(effects) {
			deferredEffect = effects[i]
		}
		a.scheduleDeferred(target, deferredEffect, call.Fun.Pos(), call.Fun.End(), state)
	}
}

func (a *functionAnalyzer) syncMapTarget(call *ast.CallExpr, receiver ast.Expr, state *flowState) (slot, bool) {
	if isSyncMap(a.checker.pass.TypesInfo.TypeOf(receiver)) {
		return a.location(receiver, state)
	}
	if target, known := a.pointerTarget(receiver, state); known {
		return target, true
	}

	selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return slot{}, false
	}
	selection := a.checker.pass.TypesInfo.Selections[selector]
	if selection == nil || selection.Kind() != types.MethodVal || len(selection.Index()) < 2 {
		return slot{}, false
	}
	target, known := a.location(selector.X, state)
	if !known {
		return slot{}, false
	}
	for _, index := range selection.Index()[:len(selection.Index())-1] {
		target.path += fieldPath(index)
	}
	return target, true
}

func (a *functionAnalyzer) scheduleDeferred(target slot, deferredEffect effect, pos, end token.Pos, state *flowState) {
	if deferredEffect == unchanged {
		return
	}
	current := state.deferred[target]
	if deferredEffect == makesUsed || current.effect == unchanged {
		state.deferred[target] = deferredStatus{effect: deferredEffect, first: pos, end: end}
	}
}

func (a *functionAnalyzer) applyDeferred(state *flowState) {
	for target, deferred := range state.deferred {
		switch deferred.effect {
		case makesUsed:
			a.markUsed(target, deferred.first, deferred.end, state)
		case makesUnknown:
			a.markUnknown(target, state)
		}
	}
}

func (a *functionAnalyzer) syncMapReceiver(call *ast.CallExpr) (ast.Expr, bool) {
	selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	selection := a.checker.pass.TypesInfo.Selections[selector]
	if selection == nil {
		return nil, false
	}
	method, _ := selection.Obj().(*types.Func)
	if method == nil || method.Pkg() == nil || method.Pkg().Path() != "sync" {
		return nil, false
	}
	if _, ok := mapMethods[method.Name()]; !ok {
		return nil, false
	}
	signature, _ := method.Type().(*types.Signature)
	if signature == nil || !isSyncMapPointer(signature.Recv().Type()) {
		return nil, false
	}
	if selection.Kind() == types.MethodExpr {
		if len(call.Args) == 0 {
			return nil, false
		}
		return call.Args[0], true
	}
	return selector.X, true
}

func (a *functionAnalyzer) isNonCopyingBuiltin(call *ast.CallExpr) bool {
	identifier, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	builtin, ok := a.checker.pass.TypesInfo.Uses[identifier].(*types.Builtin)
	if !ok {
		return false
	}
	switch builtin.Name() {
	case "len", "cap", "Sizeof", "Offsetof", "Alignof":
		return true
	default:
		return false
	}
}

func (a *functionAnalyzer) location(expression ast.Expr, state *flowState) (slot, bool) {
	switch expression := ast.Unparen(expression).(type) {
	case *ast.Ident:
		object := a.checker.pass.TypesInfo.ObjectOf(expression)
		if object == nil {
			return slot{}, false
		}
		return slot{object: object}, true
	case *ast.SelectorExpr:
		selection := a.checker.pass.TypesInfo.Selections[expression]
		if selection == nil || selection.Kind() != types.FieldVal {
			return slot{}, false
		}
		base, ok := a.location(expression.X, state)
		if !ok {
			return slot{}, false
		}
		for _, index := range selection.Index() {
			base.path += fieldPath(index)
		}
		return base, true
	case *ast.IndexExpr:
		base, ok := a.location(expression.X, state)
		if !ok {
			return slot{}, false
		}
		index, ok := constantInt(a.checker.pass.TypesInfo, expression.Index)
		if !ok {
			return slot{}, false
		}
		base.path += arrayPath(index)
		return base, true
	case *ast.StarExpr:
		target, ok := a.pointerTarget(expression.X, state)
		return target, ok
	default:
		return slot{}, false
	}
}

func (a *functionAnalyzer) pointerTarget(expression ast.Expr, state *flowState) (slot, bool) {
	switch expression := ast.Unparen(expression).(type) {
	case *ast.UnaryExpr:
		if expression.Op == token.AND {
			return a.location(expression.X, state)
		}
	case *ast.Ident:
		object := a.checker.pass.TypesInfo.ObjectOf(expression)
		pointer := state.ptrs[object]
		return pointer.target, pointer.known
	}
	return slot{}, false
}

func (a *functionAnalyzer) markUsed(target slot, pos, end token.Pos, state *flowState) {
	state.maps[target] = mapStatus{state: used, first: pos, end: end}
}

func (a *functionAnalyzer) markUnknown(target slot, state *flowState) {
	if state.maps[target].state != used {
		state.maps[target] = mapStatus{state: unknown}
	}
}

func (a *functionAnalyzer) reportCopy(expression ast.Expr, status mapStatus) {
	if !a.report || a.checker.reported[expression.Pos()] {
		return
	}
	a.checker.reported[expression.Pos()] = true
	finding := analysis.Diagnostic{
		Pos:     expression.Pos(),
		End:     expression.End(),
		Message: diagnostic,
	}
	if status.first.IsValid() {
		finding.Related = []analysis.RelatedInformation{{
			Pos:     status.first,
			End:     status.end,
			Message: "first use of this sync.Map is here",
		}}
	}
	a.checker.pass.Report(finding)
}

func (state flowState) clone() flowState {
	result := flowState{
		maps:     make(map[slot]mapStatus, len(state.maps)),
		ptrs:     make(map[types.Object]pointer, len(state.ptrs)),
		deferred: make(map[slot]deferredStatus, len(state.deferred)),
	}
	for key, status := range state.maps {
		result.maps[key] = status
	}
	for object, pointer := range state.ptrs {
		result.ptrs[object] = pointer
	}
	for target, deferred := range state.deferred {
		result.deferred[target] = deferred
	}
	return result
}

func (state *flowState) join(other flowState) bool {
	changed := false
	for key, right := range other.maps {
		left, ok := state.maps[key]
		if !ok {
			left = mapStatus{state: unknown}
		}
		joined := joinStatus(left, right)
		if !ok || joined != state.maps[key] {
			state.maps[key] = joined
			changed = true
		}
	}
	for object, right := range other.ptrs {
		left, ok := state.ptrs[object]
		joined := left
		if !ok || !left.known || !right.known || left.target != right.target {
			joined = pointer{}
		}
		if !ok || joined != left {
			state.ptrs[object] = joined
			changed = true
		}
	}
	deferredTargets := make(map[slot]struct{}, len(state.deferred)+len(other.deferred))
	for target := range state.deferred {
		deferredTargets[target] = struct{}{}
	}
	for target := range other.deferred {
		deferredTargets[target] = struct{}{}
	}
	for target := range deferredTargets {
		left := state.deferred[target]
		right := other.deferred[target]
		joined := joinDeferred(left, right)
		if joined != left {
			state.deferred[target] = joined
			changed = true
		}
	}
	return changed
}

func joinDeferred(left, right deferredStatus) deferredStatus {
	if left.effect == unchanged && right.effect == unchanged {
		return deferredStatus{}
	}
	if left.effect == makesUsed && right.effect == makesUsed {
		if left.first == right.first && left.end == right.end {
			return left
		}
		return deferredStatus{effect: makesUsed}
	}
	return deferredStatus{effect: makesUnknown}
}

func joinStatus(left, right mapStatus) mapStatus {
	if left.state == unused && right.state == unused {
		return mapStatus{state: unused}
	}
	if left.state == used && right.state == used {
		if left.first == right.first && left.end == right.end {
			return left
		}
		return mapStatus{state: used}
	}
	return mapStatus{state: unknown}
}

func initializeObject(object types.Object, initial useState, state *flowState) {
	for _, path := range mapPaths(object.Type()) {
		state.maps[slot{object: object, path: path}] = mapStatus{state: initial}
	}
}

func zeroValue(typ types.Type) value {
	paths := mapPaths(typ)
	result := value{states: make(map[string]mapStatus, len(paths))}
	for _, path := range paths {
		result.states[path] = mapStatus{state: unused}
	}
	return result
}

func unknownValue(typ types.Type) value {
	paths := mapPaths(typ)
	result := value{states: make(map[string]mapStatus, len(paths)), constructed: true}
	for _, path := range paths {
		result.states[path] = mapStatus{state: unknown}
	}
	return result
}

func mapPaths(typ types.Type) []string {
	return appendMapPaths(nil, typ, "", make(map[types.Type]bool))
}

func appendMapPaths(paths []string, typ types.Type, prefix string, seen map[types.Type]bool) []string {
	if typ == nil {
		return paths
	}
	typ = types.Unalias(typ)
	if isSyncMap(typ) {
		return append(paths, prefix)
	}
	if seen[typ] {
		return paths
	}
	seen[typ] = true
	defer delete(seen, typ)

	if named, ok := typ.(*types.Named); ok {
		typ = named.Underlying()
	}
	switch typ := typ.(type) {
	case *types.Struct:
		for i := range typ.NumFields() {
			paths = appendMapPaths(paths, typ.Field(i).Type(), prefix+fieldPath(i), seen)
		}
	case *types.Array:
		if typ.Len() > 256 {
			return paths
		}
		for i := range int(typ.Len()) {
			paths = appendMapPaths(paths, typ.Elem(), prefix+arrayPath(i), seen)
		}
	}
	return paths
}

func isSyncMap(typ types.Type) bool {
	named, ok := types.Unalias(typ).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "Map"
}

func isSyncMapPointer(typ types.Type) bool {
	pointer, ok := types.Unalias(typ).(*types.Pointer)
	return ok && isSyncMap(pointer.Elem())
}

func isPointer(typ types.Type) bool {
	_, ok := types.Unalias(typ).(*types.Pointer)
	return ok
}

func fieldPath(index int) string { return "/f" + strconv.Itoa(index) }
func arrayPath(index int) string { return "/a" + strconv.Itoa(index) }

func constantInt(info *types.Info, expression ast.Expr) (int, bool) {
	value := info.Types[expression].Value
	if value == nil {
		return 0, false
	}
	integer, exact := constant.Int64Val(value)
	return int(integer), exact && integer >= 0
}

func assignedObject(info *types.Info, expression ast.Expr) types.Object {
	identifier, ok := ast.Unparen(expression).(*ast.Ident)
	if !ok {
		return nil
	}
	if object := info.Defs[identifier]; object != nil {
		return object
	}
	return info.Uses[identifier]
}

func calledFunction(info *types.Info, expression ast.Expr) *types.Func {
	switch expression := ast.Unparen(expression).(type) {
	case *ast.Ident:
		function, _ := info.Uses[expression].(*types.Func)
		return function
	case *ast.SelectorExpr:
		if selection := info.Selections[expression]; selection != nil {
			function, _ := selection.Obj().(*types.Func)
			return function
		}
		function, _ := info.Uses[expression.Sel].(*types.Func)
		return function
	default:
		return nil
	}
}

func callSignature(info *types.Info, expression ast.Expr) *types.Signature {
	typ := info.TypeOf(expression)
	if typ == nil {
		return nil
	}
	signature, _ := typ.Underlying().(*types.Signature)
	return signature
}

func parameterAt(signature *types.Signature, index int) *types.Var {
	parameters := signature.Params()
	if index < parameters.Len() {
		return parameters.At(index)
	}
	if signature.Variadic() && parameters.Len() > 0 {
		return parameters.At(parameters.Len() - 1)
	}
	return nil
}

func selectionOf(info *types.Info, expression ast.Expr) *types.Selection {
	selector, ok := ast.Unparen(expression).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	return info.Selections[selector]
}

func isBlank(expression ast.Expr) bool {
	identifier, ok := ast.Unparen(expression).(*ast.Ident)
	return ok && identifier.Name == "_"
}
