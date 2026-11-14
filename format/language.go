package format

import (
	"fmt"
	"sort"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/env"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/parser"
)

// The language of interpretation revision 1: CEL's language definition, restricted to what follows, with the list functions it
// defines itself. Nothing outside these lists type-checks.

// Parser limits, set explicitly so that what an expression may be never follows a library default.
const (
	MaxExpressionCodePoints = 100_000
	MaxExpressionNodes      = 100_000
	MaxExpressionNesting    = 250
)

// macros are the macros of CEL's standard library an expression may use. all, exists, filter and
// map are the format's own (possibleLibrary), because they read a list with possible members; so
// is sortBy (sortByMacro).
var macros = []string{"has"}

// standardFunctions are the functions and overloads of CEL's standard library an expression may
// call, by the names and overload identifiers cel-go gives them.
var standardFunctions = map[string][]string{
	"!_":                  {"logical_not"},
	"_&&_":                {"logical_and"},
	"_||_":                {"logical_or"},
	"_?_:_":               {"conditional"},
	"@not_strictly_false": {"not_strictly_false"}, // how all and exists absorb what is undecided
	"_==_":                {"equals"},
	"_!=_":                {"not_equals"},
	"_<_":                 {"less_int64", "less_string"},
	"_<=_":                {"less_equals_int64", "less_equals_string"},
	"_>_":                 {"greater_int64", "greater_string"},
	"_>=_":                {"greater_equals_int64", "greater_equals_string"},
	"_-_":                 {"subtract_int64"},
	"_*_":                 {"multiply_int64"},
	"_/_":                 {"divide_int64"},
	"_%_":                 {"modulo_int64"},
	"-_":                  {"negate_int64"},
	"_[_]":                {"index_list", "index_map"},
	"size":                {"size_string", "size_list", "size_map", "string_size", "list_size", "map_size"},
	"contains":            {"contains_string"},
	"endsWith":            {"ends_with_string"},
	"startsWith":          {"starts_with_string"},
}

// standardLibrary is CEL's standard library, limited to the macros and functions above.
func standardLibrary() cel.EnvOption {
	subset := &env.LibrarySubset{IncludeMacros: macros}
	for _, name := range sortedKeys(standardFunctions) {
		fn := &env.Function{Name: name}
		for _, id := range standardFunctions[name] {
			fn.Overloads = append(fn.Overloads, &env.Overload{ID: id})
		}
		subset.IncludeFunctions = append(subset.IncludeFunctions, fn)
	}
	return cel.StdLib(cel.StdLibSubset(subset))
}

var (
	listOfT     = types.NewListType(typeParam)
	listOfLists = types.NewListType(listOfT)
)

// listFunctions are the list functions the format defines, with their implementations.
var listFunctions = []Function{
	{"distinct", false, 1, cel.Function("distinct",
		cel.MemberOverload("list_distinct", []*types.Type{listOfT}, listOfT, cel.UnaryBinding(distinct)))},
	{"flatten", false, 1, cel.Function("flatten",
		cel.MemberOverload("list_flatten", []*types.Type{listOfLists}, listOfT, cel.UnaryBinding(flatten)))},
	{"slice", false, 3, cel.Function("slice",
		cel.MemberOverload("list_slice", []*types.Type{listOfT, types.IntType, types.IntType}, listOfT, cel.FunctionBinding(slice)))},
	{"take", false, 2, cel.Function("take",
		cel.MemberOverload("list_take", []*types.Type{listOfT, types.IntType}, listOfT, cel.BinaryBinding(take)))},
}

// sortByKeys is what sortBy expands to: the list, ordered by keys computed for its items.
const sortByKeys = "@sort_by_keys"

func sortByLibrary() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Macros(cel.ReceiverMacro("sortBy", 2, sortByMacro)),
		cel.Function(sortByKeys,
			cel.MemberOverload("list_sort_by_int_keys", []*types.Type{listOfT, types.NewListType(types.IntType)}, listOfT, cel.BinaryBinding(sortBy)),
			cel.MemberOverload("list_sort_by_string_keys", []*types.Type{listOfT, types.NewListType(types.StringType)}, listOfT, cel.BinaryBinding(sortBy))),
	}
}

// sortByMacro expands l.sortBy(x, key) to a comprehension that binds l once and orders it by the
// keys key gives its items.
func sortByMacro(meh cel.MacroExprFactory, target celast.Expr, args []celast.Expr) (celast.Expr, *cel.Error) {
	input := meh.NewIdent("@sortBy_input")
	keys, err := parser.MakeMap(meh, meh.Copy(input), args)
	if err != nil {
		return nil, err
	}
	return meh.NewComprehension(meh.NewList(), "#unused", input.AsIdent(), target, meh.NewLiteral(types.False), input,
		meh.NewMemberCall(sortByKeys, meh.Copy(input), keys)), nil
}

func distinct(v ref.Val) ref.Val {
	if holdsPossible(v) {
		return undecided("a list with possible members cannot be compared yet")
	}
	l, ok := v.(traits.Lister)
	if !ok {
		return types.MaybeNoSuchOverloadErr(v)
	}
	var out []ref.Val
	for it := l.Iterator(); it.HasNext() == types.True; {
		item, seen := it.Next(), false
		for _, o := range out {
			if item.Equal(o) == types.True {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, item)
		}
	}
	return types.DefaultTypeAdapter.NativeToValue(out)
}

func flatten(v ref.Val) ref.Val {
	if holdsPossible(v) {
		return undecided("a list with possible members has no items in order yet")
	}
	l, ok := v.(traits.Lister)
	if !ok {
		return types.MaybeNoSuchOverloadErr(v)
	}
	var out []ref.Val
	for it := l.Iterator(); it.HasNext() == types.True; {
		inner, ok := it.Next().(traits.Lister)
		if !ok {
			return types.NewErr("flatten: an item is not a list")
		}
		for in := inner.Iterator(); in.HasNext() == types.True; {
			out = append(out, in.Next())
		}
	}
	return types.DefaultTypeAdapter.NativeToValue(out)
}

func slice(args ...ref.Val) ref.Val {
	if _, possible := args[0].(*possibleList); possible {
		return undecided("a list with possible members has no items at indexes yet")
	}
	l, ok := args[0].(traits.Lister)
	from, okFrom := args[1].(types.Int)
	to, okTo := args[2].(types.Int)
	if !ok || !okFrom || !okTo {
		return types.NoSuchOverloadErr()
	}
	size := l.Size().(types.Int)
	if from < 0 || from > to || to > size {
		return types.NewErr("slice(%d, %d) of a list of %d items", from, to, size)
	}
	var out []ref.Val
	for i := from; i < to; i++ {
		out = append(out, l.Get(i))
	}
	return types.DefaultTypeAdapter.NativeToValue(out)
}

// take is the first n items of a list, or all of them if there are fewer. Unlike slice it is
// total over lists, so a count that exceeds the list is not an error; only a negative count is.
func take(list, n ref.Val) ref.Val {
	if _, possible := list.(*possibleList); possible {
		return undecided("a list with possible members has no first items yet")
	}
	l, ok := list.(traits.Lister)
	count, okN := n.(types.Int)
	if !ok || !okN {
		return types.NoSuchOverloadErr()
	}
	if count < 0 {
		return types.NewErr("take(%d): a negative count", count)
	}
	if size := l.Size().(types.Int); count > size {
		count = size
	}
	var out []ref.Val
	for i := types.Int(0); i < count; i++ {
		out = append(out, l.Get(i))
	}
	return types.DefaultTypeAdapter.NativeToValue(out)
}

// sortBy orders a list by the keys computed for its items, ascending. Keys are integers or
// strings and must be distinct, so the order never depends on how equal keys are sorted.
func sortBy(list, keys ref.Val) ref.Val {
	if _, possible := list.(*possibleList); possible {
		return undecided("a list with possible members has no order yet")
	}
	l, okList := list.(traits.Lister)
	k, okKeys := keys.(traits.Lister)
	if !okList || !okKeys || l.Size() != k.Size() {
		return types.NoSuchOverloadErr()
	}
	n := int(l.Size().(types.Int))
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	compare := func(a, b int) types.Int {
		c, ok := k.Get(types.Int(a)).(traits.Comparer)
		if !ok {
			return types.IntZero
		}
		r, _ := c.Compare(k.Get(types.Int(b))).(types.Int)
		return r
	}
	sort.SliceStable(order, func(i, j int) bool { return compare(order[i], order[j]) == types.IntNegOne })
	for i := 1; i < n; i++ {
		if compare(order[i-1], order[i]) == types.IntZero {
			return types.NewErr("sortBy: two items have the same key %v", k.Get(types.Int(order[i])))
		}
	}
	out := make([]ref.Val, n)
	for i, o := range order {
		out[i] = l.Get(types.Int(o))
	}
	return types.DefaultTypeAdapter.NativeToValue(out)
}

// refuseOutsideTheLanguage reports what type-checking cannot: literals of types the language
// lacks, maps whose keys are not strings, list and map literals whose items differ in type, and a
// comprehension that builds a list from a map, such as map or filter over one, whose order CEL
// leaves open. all and exists, which only decide, may range over a map's keys.
func (u *Universe) refuseOutsideTheLanguage(p *Package, a *cel.Ast, where string) bool {
	native := a.NativeRep()
	ok := true
	refuse := func(format string, args ...any) {
		u.errorf(p, where, format, args...)
		ok = false
	}
	celast.PostOrderVisit(native.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if t := native.GetType(e.ID()); t != nil && t.Kind() == types.MapKind && t.Parameters()[0].Kind() != types.StringKind {
			refuse("has a map whose keys are %s; a map's keys are strings", t.Parameters()[0])
		}
		switch e.Kind() {
		case celast.ListKind:
			mixed(native, e.AsList().Elements(), refuse)
		case celast.MapKind:
			var values []celast.Expr
			for _, entry := range e.AsMap().Entries() {
				values = append(values, entry.AsMapEntry().Value())
			}
			mixed(native, values, refuse)
		case celast.LiteralKind:
			switch e.AsLiteral().(type) {
			case types.Bool, types.Int, types.String:
			default:
				refuse("has a %s literal, which interpretation revision %d does not allow: only booleans, integers and strings", e.AsLiteral().Type().TypeName(), Interpretation)
			}
		case celast.ComprehensionKind:
			if collectsStatuses(e.AsComprehension()) {
				break // all and exists range over a map's keys; filter and map are checked where they read it
			}
			r := native.GetType(e.AsComprehension().IterRange().ID())
			if t := native.GetType(e.ID()); r != nil && r.Kind() == types.MapKind && t != nil && t.Kind() != types.BoolKind {
				refuse("builds a list from a map, whose order is not defined; map, filter and sortBy range over lists only")
			}
		case celast.CallKind:
			if fn := e.AsCall().FunctionName(); fn == filterFn || fn == mapFn {
				if r := native.GetType(e.AsCall().Args()[0].ID()); r != nil && r.Kind() == types.MapKind {
					refuse("builds a list from a map, whose order is not defined; map, filter and sortBy range over lists only")
				}
			}
		}
	}))
	return ok
}

// collectsStatuses reports whether a comprehension is the one a macro of possibleLibrary uses to
// collect the status of a predicate for each item, which builds a list of statuses and not a
// list of items.
func collectsStatuses(comp celast.ComprehensionExpr) bool {
	step := comp.LoopStep()
	if step.Kind() != celast.CallKind || len(step.AsCall().Args()) != 2 {
		return false
	}
	items := step.AsCall().Args()[1]
	if items.Kind() != celast.ListKind || len(items.AsList().Elements()) != 1 {
		return false
	}
	item := items.AsList().Elements()[0]
	return item.Kind() == celast.CallKind && item.AsCall().FunctionName() == predicateFn
}

// mixed refuses a list or map literal whose items have different types, where both are known.
func mixed(native *celast.AST, items []celast.Expr, refuse func(string, ...any)) {
	var first *types.Type
	for _, item := range items {
		t := native.GetType(item.ID())
		if t == nil || t.Kind() == types.DynKind {
			continue
		}
		if first == nil {
			first = t
		} else if !t.IsExactType(first) {
			refuse("has a literal whose items are both %s and %s; a list's items, and a map's values, have one type", first, t)
			return
		}
	}
}

// absent is what reading an absent field of type t gives: an empty list or map, or an empty
// string for a string-valued type, which no set value can be; any other absent value is
// unavailable.
func absent(t *Type) (any, bool) {
	if t == nil {
		return nil, false
	}
	switch t.Kind {
	case "list":
		return []any{}, true
	case "map":
		return map[string]any{}, true
	case "enum":
		return "", true
	case "scalar":
		switch t.Name {
		case "string", "text", "digest", "name", "name-pattern", "location", "principal":
			return "", true
		}
	case "named":
		if t.Decl != nil && t.Decl.Kind == "enum" {
			return "", true
		}
	}
	return nil, false
}

// fieldValue reads field f of an instance held as a map, as the format's absent values define.
// A field of an instance not known yet, such as a possible member whose mapping waits, is not
// known either.
func fieldValue(obj any, f *Field) (any, error) {
	if u, ok := obj.(*types.Unknown); ok {
		return u, nil
	}
	if v, ok := obj.(ref.Val); ok {
		obj = v.Value()
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not an instance", f.Name)
	}
	v, set := m[f.Name]
	if !set {
		if empty, ok := absent(f.Type); ok {
			return empty, nil
		}
		return nil, fmt.Errorf("%s is absent", f.Name)
	}
	if !f.Enabled {
		return nil, fmt.Errorf("%s is not enabled", f.Name)
	}
	if f.Type != nil && f.Type.Kind == "scalar" && f.Type.Name == "time" {
		if s, ok := v.(string); ok {
			return ParseTime(s)
		}
	}
	return v, nil
}

func fieldIsSet(obj any, f *Field) bool {
	if v, ok := obj.(ref.Val); ok {
		obj = v.Value()
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return false
	}
	_, set := m[f.Name]
	return set
}
