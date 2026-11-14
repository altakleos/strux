package format

import (
	"fmt"
	"reflect"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/decls"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/interpreter"
	"cel.dev/cel-go/parser"
)

// Possible members.
//
// A list a log-reading function returns lists a revision whose fact is pending or indeterminate
// as a possible member: a member if its fact comes to be recognized, and nothing yet says whether
// it will. The reference implementation holds no log, so a conformance vector marks the items of
// a list it gives as possible. A list that holds one is a possibleList. A list that holds none is
// an ordinary list, and every macro, function and operator below gives it the result it always
// gave.
//
// An unknown either met an error or did not, and the rules below read which. An unknown that met
// an error is a *types.Err; one that met none, such as what waits on a possible member, is a
// *types.Unknown (undecided). CEL's strict calls return an error over an unknown whichever
// operand holds it, and strictOperands makes && and || do the same, so what an evaluation meets
// never depends on the order of its operands.
//
// The rules are the existing absorption rules applied to membership:
//
//   - exists is true when a certain member satisfies the predicate, false when the predicate is
//     false for every item, and unknown otherwise; all is false when a certain member fails it,
//     true when every item satisfies it, and unknown otherwise; x in l is exists with equality;
//   - filter drops an item the predicate is false for and keeps one it is true for, a possible
//     member as a possible member; a possible member the predicate is unknown for without an
//     error stays a possible member; the result is unknown when the predicate is unknown for a
//     certain member, or met an error for any item;
//   - map keeps each item's status; a possible member whose mapping is unknown without an error
//     stays a possible member whose value is unknown; the result is unknown when the mapping is
//     unknown for a certain member, or met an error for any item; + keeps each item's status;
//   - everything else (size, indexing, slice, take, distinct, flatten, sortBy, == and !=) is
//     unknown, without an error.

// possibleList is a list some of whose items are possible members. It can be iterated, and its
// size and its item at an index are unknown without an error, so every operator that needs more
// than iteration is unknown.
type possibleList struct {
	items    []ref.Val
	possible []bool // possible[i] is true when item i is a possible member
}

var possibleListType = types.NewObjectType("possible_list", traits.IterableType, traits.AdderType, traits.SizerType, traits.IndexerType)

// undecided is an unknown that met no error, with why it is unknown.
func undecided(format string, args ...any) ref.Val {
	return types.NewUnknown(0, types.NewAttributeTrail(fmt.Sprintf(format, args...)))
}

// reasonOf says why an unknown that met no error is unknown.
func reasonOf(u *types.Unknown) string {
	var reasons []string
	for _, id := range u.IDs() {
		trails, _ := u.GetAttributeTrails(id)
		for _, t := range trails {
			reasons = append(reasons, t.Variable())
		}
	}
	return strings.Join(reasons, "; ")
}

// unknownOf is what an evaluation whose operands include an unknown gives: an error any of them
// met, whichever operand holds it, or else every unknown merged.
func unknownOf(vals ...ref.Val) ref.Val {
	var unknown *types.Unknown
	for _, v := range vals {
		if types.IsError(v) {
			return v
		}
		unknown, _ = types.MaybeMergeUnknowns(v, unknown)
	}
	if unknown != nil {
		return unknown
	}
	return types.NewErr("no such overload")
}

// PossibleList is a list some of whose items are possible members, given by the instance of a
// conformance vector. Its items are the values the instance holds; possible gives the indexes of
// those that are possible members.
func PossibleList(items []any, possible []int) (any, error) {
	l := &possibleList{possible: make([]bool, len(items))}
	for _, item := range items {
		l.items = append(l.items, types.DefaultTypeAdapter.NativeToValue(item))
	}
	for _, i := range possible {
		if i < 0 || i >= len(items) {
			return nil, fmt.Errorf("item %d is marked possible, but the list has %d items", i, len(items))
		}
		l.possible[i] = true
	}
	if len(possible) == 0 {
		return items, nil // a list with no possible member is an ordinary list
	}
	return l, nil
}

func (l *possibleList) ConvertToNative(reflect.Type) (any, error) {
	return nil, fmt.Errorf("a list with possible members has no value yet")
}
func (l *possibleList) ConvertToType(ref.Type) ref.Val {
	return undecided("a list with possible members has no value yet")
}
func (l *possibleList) Equal(ref.Val) ref.Val {
	return undecided("a list with possible members cannot be compared yet")
}
func (l *possibleList) Type() ref.Type { return possibleListType }
func (l *possibleList) Value() any     { return l }

// Size and Get implement traits.Sizer and traits.Indexer, so that size and indexing reach the
// list and are unknown without an error. A possibleList is not a traits.Lister, since it has no
// Contains, so nothing reads it as an ordinary list.
func (l *possibleList) Size() ref.Val {
	return undecided("a list with possible members has no size yet")
}
func (l *possibleList) Get(ref.Val) ref.Val {
	return undecided("a list with possible members has no item at an index yet")
}

// Iterator implements traits.Iterable: the comprehensions range over the items, and the macros
// below read each item's status from the list.
func (l *possibleList) Iterator() traits.Iterator {
	return types.NewRefValList(types.DefaultTypeAdapter, l.items).Iterator()
}

// statusOf reads the items of a list-like value and which of them are possible members. A value
// that is not a list yields its keys, which all and exists range over, as ordinary members.
func statusOf(v ref.Val) (items []ref.Val, possible []bool, ok bool) {
	switch l := v.(type) {
	case *possibleList:
		return l.items, l.possible, true
	case traits.Lister:
		n := int(l.Size().(types.Int))
		for i := 0; i < n; i++ {
			items = append(items, l.Get(types.Int(i)))
		}
		return items, make([]bool, n), true
	case traits.Mapper:
		for it := l.Iterator(); it.HasNext() == types.True; {
			items = append(items, it.Next())
		}
		return items, make([]bool, len(items)), true
	}
	return nil, nil, false
}

// holdsPossible reports whether a value is, or holds at any depth in a list or a map, a list with
// possible members. Such a value has no value of its own yet, so a comparison, a membership test or
// a search for duplicates that reaches one is unknown, however deep it lies.
func holdsPossible(v ref.Val) bool {
	switch x := v.(type) {
	case *possibleList:
		return true
	case traits.Lister:
		n, ok := x.Size().(types.Int)
		if !ok {
			return false
		}
		for i := types.Int(0); i < n; i++ {
			if holdsPossible(x.Get(i)) {
				return true
			}
		}
	case traits.Mapper:
		for it := x.Iterator(); it.HasNext() == types.True; {
			key := it.Next()
			if holdsPossible(key) || holdsPossible(x.Get(key)) {
				return true
			}
		}
	}
	return false
}

// Statuses of a predicate for one item, as the macros collect them.
const (
	predicateFalse   = 0
	predicateTrue    = 1
	predicateUnknown = 2 // unknown without an error
	predicateError   = 3 // unknown, having met an error
)

// predicateStatus reads the result of a predicate for one item, which may be unknown.
func predicateStatus(v ref.Val) ref.Val {
	switch b := v.(type) {
	case types.Bool:
		if b {
			return types.Int(predicateTrue)
		}
		return types.Int(predicateFalse)
	case *types.Unknown:
		return types.Int(predicateUnknown)
	}
	return types.Int(predicateError)
}

// statusesOf reads the predicate statuses a macro collected, which line up with the items.
func statusesOf(source, statuses ref.Val) (items []ref.Val, possible []bool, status []int64, err ref.Val) {
	items, possible, ok := statusOf(source)
	list, isList := statuses.(traits.Lister)
	if !ok || !isList || int(list.Size().(types.Int)) != len(items) {
		return nil, nil, nil, types.NewErr("no such overload")
	}
	for i := range items {
		n, isInt := list.Get(types.Int(i)).(types.Int)
		if !isInt {
			return nil, nil, nil, types.NewErr("no such overload")
		}
		status = append(status, int64(n))
	}
	return items, possible, status, nil
}

// undecidedOver is the unknown a macro gives when no item decides it: one that met an error
// where its predicate met one for any item, and otherwise one that met none.
func undecidedOver(status []int64, macro, why string) ref.Val {
	for _, s := range status {
		if s == predicateError {
			return types.NewErr("%s is unknown: its predicate met an error for an item", macro)
		}
	}
	return undecided("%s is undecided: %s", macro, why)
}

// existsOf decides exists from the statuses of its predicate: true when a certain member
// satisfies it, false when it is false for every item, possible members included.
func existsOf(source, statuses ref.Val) ref.Val {
	_, possible, status, err := statusesOf(source, statuses)
	if err != nil {
		return err
	}
	falseForAll := true
	for i, s := range status {
		if s == predicateTrue && !possible[i] {
			return types.True
		}
		falseForAll = falseForAll && s == predicateFalse
	}
	if falseForAll {
		return types.False
	}
	return undecidedOver(status, "exists", "an item may or may not satisfy the predicate")
}

// allOf decides all from the statuses of its predicate.
func allOf(source, statuses ref.Val) ref.Val {
	_, possible, status, err := statusesOf(source, statuses)
	if err != nil {
		return err
	}
	trueForAll := true
	for i, s := range status {
		if s == predicateFalse && !possible[i] {
			return types.False
		}
		trueForAll = trueForAll && s == predicateTrue
	}
	if trueForAll {
		return types.True
	}
	return undecidedOver(status, "all", "an item may or may not satisfy the predicate")
}

// filterOf drops the items whose predicate is false and keeps those it is true for, and a
// possible member whose predicate is unknown without an error as a possible member. A predicate
// that met an error for any item, or is unknown for a certain member, makes the result unknown.
func filterOf(source, statuses ref.Val) ref.Val {
	items, possible, status, err := statusesOf(source, statuses)
	if err != nil {
		return err
	}
	out := &possibleList{}
	certainUnknown := false
	for i, s := range status {
		switch {
		case s == predicateFalse:
		case s == predicateError:
			return types.NewErr("filter is unknown: its predicate met an error for an item")
		case s == predicateUnknown && !possible[i]:
			certainUnknown = true
		default:
			out.items, out.possible = append(out.items, items[i]), append(out.possible, possible[i])
		}
	}
	if certainUnknown {
		return undecided("filter is undecided: the predicate is unknown for a member")
	}
	return out.collapse()
}

// mapOf gives the mapped list the statuses of the items it was mapped from. The mapping of each
// item arrives in a slot, so that an unknown one does not spoil the whole list: a possible member
// whose mapping is unknown without an error stays a possible member whose value is unknown. A
// mapping that met an error for any item, or is unknown for a certain member, makes the result
// unknown.
func mapOf(source, mapped ref.Val) ref.Val {
	items, possible, ok := statusOf(source)
	list, isList := mapped.(traits.Lister)
	if !ok || !isList || int(list.Size().(types.Int)) != len(items) {
		return types.NewErr("no such overload")
	}
	out := &possibleList{possible: possible}
	var unknowns []ref.Val
	for i := range items {
		v := unslot(list.Get(types.Int(i)))
		switch {
		case types.IsError(v):
			return v
		case types.IsUnknown(v) && !possible[i]:
			unknowns = append(unknowns, v)
		}
		out.items = append(out.items, v)
	}
	if len(unknowns) > 0 {
		return unknownOf(unknowns...)
	}
	return out.collapse()
}

// slot holds the mapping of one item while map collects them, so that an unknown mapping reaches
// mapOf instead of making the list being built unknown.
type slot struct{ held ref.Val }

var slotType = types.NewObjectType("@slot")

func (s slot) ConvertToNative(reflect.Type) (any, error) {
	return nil, fmt.Errorf("a mapping held for map")
}
func (s slot) ConvertToType(ref.Type) ref.Val { return types.NewErr("a mapping held for map") }
func (s slot) Equal(ref.Val) ref.Val          { return types.NewErr("a mapping held for map") }
func (s slot) Type() ref.Type                 { return slotType }
func (s slot) Value() any                     { return s.held }

// holdMapping is the non-strict function a mapping is wrapped in: it passes a value through, and
// holds an unknown one in a slot.
func holdMapping(v ref.Val) ref.Val {
	if types.IsUnknownOrError(v) {
		return slot{v}
	}
	return v
}

func unslot(v ref.Val) ref.Val {
	if s, ok := v.(slot); ok {
		return s.held
	}
	return v
}

// collapse gives an ordinary list when no item is a possible member.
func (l *possibleList) collapse() ref.Val {
	for _, p := range l.possible {
		if p {
			return l
		}
	}
	return types.NewRefValList(types.DefaultTypeAdapter, l.items)
}

// addLists concatenates two lists, keeping each item's status, and adds anything else as CEL does.
func addValues(lhs, rhs ref.Val) ref.Val {
	_, lPossible := lhs.(*possibleList)
	_, rPossible := rhs.(*possibleList)
	if !lPossible && !rPossible {
		return lhs.(traits.Adder).Add(rhs)
	}
	li, lp, lok := statusOf(lhs)
	ri, rp, rok := statusOf(rhs)
	if !lok || !rok {
		return types.NewErr("no such overload")
	}
	out := &possibleList{items: append(append([]ref.Val{}, li...), ri...), possible: append(append([]bool{}, lp...), rp...)}
	return out.collapse()
}

// inValues is membership, which is exists with equality, and otherwise CEL's.
func inValues(lhs, rhs ref.Val) ref.Val {
	l, ok := rhs.(*possibleList)
	if !ok {
		if holdsPossible(lhs) || holdsPossible(rhs) {
			return undecided("a list with possible members cannot be compared yet")
		}
		if rhs.Type().HasTrait(traits.ContainerType) {
			return rhs.(traits.Container).Contains(lhs)
		}
		return types.ValOrErr(rhs, "no such overload")
	}
	open := false
	for i, item := range l.items {
		if types.IsUnknown(item) || holdsPossible(lhs) || holdsPossible(item) {
			open = true // a possible member whose value is not known yet, or a comparison that waits
			continue
		}
		switch eq := types.Equal(lhs, item); {
		case eq == types.True && !l.possible[i]:
			return types.True
		case eq != types.False:
			open = true
		}
	}
	if open {
		return undecided("in is undecided: a possible member may or may not equal the value")
	}
	return types.False
}

// possibleLibrary declares the macros and functions that read a list with possible members. It
// replaces CEL's all, exists, filter and map, whose results over such a list differ, and its list
// concatenation and membership test. Each macro collects the status of its predicate for every
// item with CEL's own map comprehension, and a function then decides the result from the statuses
// and from which items are possible members.
func possibleLibrary() []cel.EnvOption {
	list := types.NewListType(typeParam)
	held := types.NewTypeParamType("H")
	otherList := types.NewListType(types.NewTypeParamType("U"))
	mapOfTV := types.NewMapType(typeParam, types.NewTypeParamType("V"))
	ints := types.NewListType(types.IntType)
	return []cel.EnvOption{
		cel.Macros(
			cel.ReceiverMacro("all", 2, quantifierMacro(allFn)),
			cel.ReceiverMacro("exists", 2, quantifierMacro(existsFn)),
			cel.ReceiverMacro("filter", 2, filterMacro),
			cel.ReceiverMacro("map", 2, mapMacro),
			cel.ReceiverMacro("map", 3, mapFilterMacro)),
		cel.Function(predicateFn,
			cel.Overload("possible_predicate_bool", []*types.Type{types.BoolType}, types.IntType,
				cel.UnaryBinding(predicateStatus), cel.OverloadIsNonStrict())),
		cel.Function(existsFn,
			cel.Overload("possible_exists_list", []*types.Type{list, ints}, types.BoolType),
			cel.Overload("possible_exists_map", []*types.Type{mapOfTV, ints}, types.BoolType),
			decls.SingletonBinaryBinding(existsOf)),
		cel.Function(allFn,
			cel.Overload("possible_all_list", []*types.Type{list, ints}, types.BoolType),
			cel.Overload("possible_all_map", []*types.Type{mapOfTV, ints}, types.BoolType),
			decls.SingletonBinaryBinding(allOf)),
		cel.Function(filterFn,
			cel.Overload("possible_filter_list", []*types.Type{list, ints}, list),
			cel.Overload("possible_filter_map", []*types.Type{mapOfTV, ints}, types.NewListType(typeParam)),
			decls.SingletonBinaryBinding(filterOf)),
		cel.Function(mapFn,
			cel.Overload("possible_map_list", []*types.Type{list, otherList}, otherList),
			cel.Overload("possible_map_map", []*types.Type{mapOfTV, otherList}, otherList),
			decls.SingletonBinaryBinding(mapOf)),
		cel.Function("_+_",
			cel.Overload("add_int64", []*types.Type{types.IntType, types.IntType}, types.IntType),
			cel.Overload("add_string", []*types.Type{types.StringType, types.StringType}, types.StringType),
			cel.Overload("add_list", []*types.Type{list, list}, list),
			decls.SingletonBinaryBinding(addValues, traits.AdderType)),
		cel.Function("@in",
			cel.Overload("in_list", []*types.Type{typeParam, list}, types.BoolType),
			cel.Overload("in_map", []*types.Type{typeParam, types.NewMapType(typeParam, types.NewTypeParamType("V"))}, types.BoolType),
			decls.SingletonBinaryBinding(inValues)),
		cel.Function(holdFn,
			cel.Overload("possible_hold", []*types.Type{held}, held,
				cel.UnaryBinding(holdMapping), cel.OverloadIsNonStrict())),
		cel.Function(andFn,
			cel.Overload("strict_operands_and", []*types.Type{types.BoolType, types.BoolType}, types.BoolType,
				cel.BinaryBinding(logical(types.False)), cel.OverloadIsNonStrict())),
		cel.Function(orFn,
			cel.Overload("strict_operands_or", []*types.Type{types.BoolType, types.BoolType}, types.BoolType,
				cel.BinaryBinding(logical(types.True)), cel.OverloadIsNonStrict())),
	}
}

// The names of the functions the macros expand to. None can be written in an expression.
const (
	predicateFn = "@predicate"
	existsFn    = "@exists"
	allFn       = "@all"
	filterFn    = "@filter"
	mapFn       = "@map"
	holdFn      = "@hold"
	andFn       = "@and"
	orFn        = "@or"
)

// statuses is CEL's map comprehension over the target, collecting the status of the predicate.
func statuses(meh cel.MacroExprFactory, target celast.Expr, v, predicate celast.Expr) (celast.Expr, *cel.Error) {
	return parser.MakeMap(meh, target, []celast.Expr{v, meh.NewCall(predicateFn, predicate)})
}

// bindOnce expands to a comprehension that binds the target once, as sortByMacro does, so that a
// macro reads its receiver a single time. Reading it twice, as the receiver of the call and as the
// range of the status comprehension, doubled the size of the expansion at each chained macro.
func bindOnce(meh cel.MacroExprFactory, name string, target celast.Expr, build func(input celast.Expr) (celast.Expr, *cel.Error)) (celast.Expr, *cel.Error) {
	input := meh.NewIdent(name)
	result, err := build(input)
	if err != nil {
		return nil, err
	}
	return meh.NewComprehension(meh.NewList(), "#unused", input.AsIdent(), target, meh.NewLiteral(types.False), meh.Copy(input), result), nil
}

func quantifierMacro(fn string) cel.MacroFactory {
	return func(meh cel.MacroExprFactory, target celast.Expr, args []celast.Expr) (celast.Expr, *cel.Error) {
		return bindOnce(meh, "@"+fn[1:]+"_input", target, func(input celast.Expr) (celast.Expr, *cel.Error) {
			collected, err := statuses(meh, meh.Copy(input), args[0], args[1])
			if err != nil {
				return nil, err
			}
			return meh.NewCall(fn, meh.Copy(input), collected), nil
		})
	}
}

func filterMacro(meh cel.MacroExprFactory, target celast.Expr, args []celast.Expr) (celast.Expr, *cel.Error) {
	return bindOnce(meh, "@filter_input", target, func(input celast.Expr) (celast.Expr, *cel.Error) {
		collected, err := statuses(meh, meh.Copy(input), args[0], args[1])
		if err != nil {
			return nil, err
		}
		return meh.NewCall(filterFn, meh.Copy(input), collected), nil
	})
}

func mapMacro(meh cel.MacroExprFactory, target celast.Expr, args []celast.Expr) (celast.Expr, *cel.Error) {
	return bindOnce(meh, "@map_input", target, func(input celast.Expr) (celast.Expr, *cel.Error) {
		mapped, err := parser.MakeMap(meh, meh.Copy(input), []celast.Expr{args[0], meh.NewCall(holdFn, args[1])})
		if err != nil {
			return nil, err
		}
		return meh.NewCall(mapFn, meh.Copy(input), mapped), nil
	})
}

// mapFilterMacro is l.map(x, p, f), which is l.filter(x, p).map(x, f).
func mapFilterMacro(meh cel.MacroExprFactory, target celast.Expr, args []celast.Expr) (celast.Expr, *cel.Error) {
	kept, err := filterMacro(meh, target, args[:2])
	if err != nil {
		return nil, err
	}
	return mapMacro(meh, kept, []celast.Expr{meh.Copy(args[0]), args[2]})
}

// logical is && when decides is false and || when it is true: a side that decides gives the
// result whatever the other is, two sides that do not give the other value, and otherwise the
// result is unknown, having met an error when either side met one. Both sides are evaluated, so
// that what the evaluation meets does not depend on their order.
func logical(decides types.Bool) func(lhs, rhs ref.Val) ref.Val {
	return func(lhs, rhs ref.Val) ref.Val {
		if lhs == decides || rhs == decides {
			return decides
		}
		if lhs == !decides && rhs == !decides {
			return !decides
		}
		return unknownOf(lhs, rhs)
	}
}

// strictOperands rewrites && and || into @and and @or before an expression is evaluated, since
// cel-go's own operators return an unknown over an error and so lose that the evaluation met
// one. The expression type-checks and lints as written; only what is evaluated is rewritten.
func strictOperands(env *cel.Env, a *cel.Ast) (*cel.Ast, error) {
	opt, err := cel.NewStaticOptimizer(strictLogic{})
	if err != nil {
		return nil, err
	}
	out, iss := opt.Optimize(env, a)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	return out, nil
}

type strictLogic struct{}

func (strictLogic) Optimize(ctx *cel.OptimizerContext, a *celast.AST) *celast.AST {
	celast.PostOrderVisit(a.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.CallKind {
			return
		}
		call := e.AsCall()
		fn := ""
		switch call.FunctionName() {
		case operators.LogicalAnd:
			fn = andFn
		case operators.LogicalOr:
			fn = orFn
		default:
			return
		}
		ctx.UpdateExpr(e, ctx.NewCall(fn, call.Args()...))
	}))
	return a
}

// equalityDecorator makes == and != unknown when either side is a list with possible members. CEL
// plans them without consulting a function, and an ordinary list would decide a comparison with the
// list on its right.
func equalityDecorator() cel.ProgramOption {
	return cel.CustomDecoratorV2(func(i interpreter.InterpretableV2) (interpreter.InterpretableV2, error) {
		call, ok := i.(interpreter.InterpretableCall)
		if !ok || (call.Function() != "_==_" && call.Function() != "_!=_") || len(call.Args()) != 2 {
			return i, nil
		}
		return &possibleEquality{call, call.Function() == "_!=_"}, nil
	})
}

type possibleEquality struct {
	interpreter.InterpretableCall
	negate bool
}

func (e *possibleEquality) Eval(ctx interpreter.Activation) ref.Val {
	return e.Exec(interpreter.AsFrame(ctx))
}

func (e *possibleEquality) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	args := e.Args()
	lhs := args[0].Exec(frame)
	if types.IsError(lhs) {
		return lhs
	}
	rhs := args[1].Exec(frame)
	if types.IsError(rhs) {
		return rhs
	}
	var unknown *types.Unknown
	unknown, _ = types.MaybeMergeUnknowns(lhs, unknown)
	unknown, _ = types.MaybeMergeUnknowns(rhs, unknown)
	if unknown != nil {
		return unknown
	}
	if holdsPossible(lhs) || holdsPossible(rhs) {
		return undecided("a list with possible members cannot be compared yet")
	}
	if e.negate {
		return types.Bool(types.Equal(lhs, rhs) != types.True)
	}
	return types.Equal(lhs, rhs)
}
