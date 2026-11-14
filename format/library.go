package format

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

// builtinPackage is the namespace of the format's own types and records, such as strux.fact. It
// belongs to the format and names no package of a tree; the kernel is named by the record.
const builtinPackage = "strux"

// Interpretation is the interpretation revision this package implements.
const Interpretation = 1

// CostLimit is the checker's bound on a formal check's worst case, in cel-go cost units: a rule
// of this repository that keeps every formal check cheap enough for any verifier to finish, not
// part of the interpretation, under which an expression's value never depends on its cost.
const CostLimit = 1_000_000_000

// QuotedTextMax is the most text quoted reads at a location; a longer citation is unavailable.
const QuotedTextMax = 65536

var (
	typeParam   = types.NewTypeParamType("T")
	refOfT      = types.NewOpaqueType(builtinPackage+".ref", typeParam)
	factType    = types.NewObjectType(builtinPackage + ".fact")
	combineType = types.NewObjectType(builtinPackage + ".combination")
	stringList  = types.NewListType(types.StringType)
)

// RefType is the expression type of an exact reference to a value of type t.
func RefType(t *types.Type) *types.Type { return types.NewOpaqueType(builtinPackage+".ref", t) }

// Function is one library function: its name, whether it reads the history (and so is
// available only to the conditions of uses), and its declaration.
type Function struct {
	Name  string
	State bool
	Arity int           // arguments, counting a member function's receiver
	Decl  cel.EnvOption // nil for a function the universe declares, because its value depends on the packages read
}

// Library lists the functions expressions may call beyond what CEL's standard library gives
// them: the list functions the format defines, and the functions that read references, retained
// bytes and the history.
var Library = append(listFunctions, []Function{
	{"content", false, 1, cel.Function("content", cel.MemberOverload("ref_content", []*types.Type{refOfT}, typeParam))},
	{"fact", false, 1, cel.Function("fact", cel.MemberOverload("ref_fact", []*types.Type{refOfT}, factType))},
	{"digest", false, 1, cel.Function("digest", cel.MemberOverload("ref_digest", []*types.Type{refOfT}, types.StringType))},
	{"retained", false, 1, cel.Function("retained", cel.Overload("retained_string", []*types.Type{types.StringType}, types.BoolType))},
	{"quoted", false, 3, cel.Function("quoted", cel.Overload("quoted_string_string_string", []*types.Type{types.StringType, types.StringType, types.StringType}, types.BoolType))},
	{"matches_name", false, 2, cel.Function("matches_name", cel.Overload("matches_name_string_string", []*types.Type{types.StringType, types.StringType}, types.BoolType, cel.BinaryBinding(matchesName)))},
	{"combination_of", false, 1, nil}, // declared by Universe.environments, which resolves the check it names
	{"combine", false, 2, nil},        // declared by Universe.environments, which knows the kernel's finding type
	{"history", true, 1, cel.Function("history", cel.Overload("history_type", []*types.Type{types.NewTypeTypeWithParam(typeParam)}, types.NewListType(refOfT)))},
	{"recognized", true, 1, cel.Function("recognized", cel.Overload("recognized_ref", []*types.Type{refOfT}, types.BoolType))},
	{"current", true, 1, cel.Function("current", cel.Overload("current_ref", []*types.Type{refOfT}, refOfT))},
	{"in_force", true, 1, cel.Function("in_force", cel.Overload("in_force_ref", []*types.Type{refOfT}, types.BoolType))},
	{"authorized", true, 2, cel.Function("authorized", cel.Overload("authorized_string_string", []*types.Type{types.StringType, types.StringType}, types.BoolType))},
	{"outside_reach", true, 2, cel.Function("outside_reach", cel.Overload("outside_reach_string_list", []*types.Type{types.StringType, stringList}, types.BoolType))},
	{"holds", true, 2, cel.Function("holds", cel.Overload("holds_ref_string", []*types.Type{refOfT, types.StringType}, types.BoolType))},
	{"permitted", true, 2, cel.Function("permitted", cel.Overload("permitted_ref_string", []*types.Type{refOfT, types.StringType}, types.BoolType))},
	{"findings", true, 2, nil},           // declared by Universe.environments, like combine
	{"candidate_findings", true, 2, nil}, // declared by Universe.environments, like combine
}...)

// Capabilities are the steps of verification the interpreter performs itself, which a condition may
// name with interpreter.
var Capabilities = []string{
	"verify.trust-root",
	"verify.bootstrap",
	"verify.anchors",
	"verify.definitions",
	"verify.package",
	"verify.signature",
	"verify.compromise",
	"verify.time",
	"verify.authority",
	"verify.conditions",
	"verify.order",
	"verify.supply-chain",
}

// combine decides whether findings combine to satisfied under a combination, as the format's
// combination rules say: it takes the first cap findings in the order given, and gives true when
// they combine to satisfied, false when they combine to unsatisfied, and unknown otherwise. No
// findings, and so a cap of zero, is unknown. Such an unknown is the function's own result and
// meets no error; a finding it cannot read is an error. The reference implementation holds no log, so it
// reads a finding from its content, a map with its result, as a conformance vector gives it.
func combine(findings, combination ref.Val) ref.Val {
	if l, ok := findings.(*possibleList); ok {
		return combinePossible(l, combination)
	}
	list, ok := findings.(traits.Lister)
	if !ok {
		return types.NewErr("combine: the findings are not a list")
	}
	rule, limit, err := combinationOf(combination)
	if err != nil {
		return types.WrapErr(err)
	}
	if limit < 0 {
		return types.NewErr("combine: a negative cap")
	}
	size := int64(list.Size().(types.Int))
	if limit > size {
		limit = size
	}
	var satisfied, unsatisfied int64
	for i := int64(0); i < limit; i++ {
		switch result := resultOf(list.Get(types.Int(i))); result {
		case "satisfied":
			satisfied++
		case "unsatisfied":
			unsatisfied++
		case "unknown":
		default:
			return types.NewErr("combine: finding %d has no readable result", i)
		}
	}
	if limit == 0 {
		return undecided("combine: no findings")
	}
	var yes, no bool
	switch rule {
	case "unanimous":
		yes, no = satisfied == limit, unsatisfied == limit
	case "all-satisfied":
		yes, no = satisfied == limit, unsatisfied > 0
	case "majority":
		yes, no = 2*satisfied > limit, 2*unsatisfied >= limit
	default:
		return types.NewErr("combine: no combination rule %q", rule)
	}
	switch {
	case yes:
		return types.True
	case no:
		return types.False
	}
	return undecided("combine: the findings do not settle")
}

// combinePossible combines a list with possible members. It is decided only if no possible member
// lies among the first cap findings, because a possible finding may or may not be taken; otherwise
// it is the combination of the findings taken, which every way of deciding the pending facts gives.
func combinePossible(l *possibleList, combination ref.Val) ref.Val {
	_, limit, err := combinationOf(combination)
	if err != nil {
		return types.WrapErr(err)
	}
	if limit < 0 {
		return types.NewErr("combine: a negative cap")
	}
	if limit > int64(len(l.items)) {
		limit = int64(len(l.items))
	}
	for i := int64(0); i < limit; i++ {
		if l.possible[i] {
			return undecided("combine: finding %d is a possible member, so the findings taken are not yet known", i)
		}
	}
	return combine(types.NewRefValList(types.DefaultTypeAdapter, l.items[:limit]), combination)
}

// combinationOf reads the rule and the cap of a combination value.
func combinationOf(v ref.Val) (string, int64, error) {
	m, ok := v.Value().(map[string]any)
	if !ok {
		return "", 0, fmt.Errorf("combine: not a combination")
	}
	rule, _ := m["rule"].(string)
	switch n := m["cap"].(type) {
	case int64:
		return rule, n, nil
	case int:
		return rule, int64(n), nil
	}
	return "", 0, fmt.Errorf("combine: the combination has no cap")
}

// resultOf reads the result of a finding given by its content.
func resultOf(v ref.Val) string {
	if m, ok := v.Value().(map[string]any); ok {
		s, _ := m["result"].(string)
		return s
	}
	return ""
}
