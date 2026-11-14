package format

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"
)

// Result is what an expression evaluates to: true, false or unknown, with why it is unknown.
// The reason is informative; two evaluations agree when their values do. Whether an unknown met
// an error is not part of a Result: only the possible-member rules read it, inside an evaluation.
type Result struct {
	Value  string // "true", "false" or "unknown"
	Reason string
}

// EvaluateCheck evaluates expr as a formal check of definition def in package pkg, with the named
// lets, on an instance held as it is parsed. It reads only the instance, as a formal check does,
// and returns the problems that keep it from type-checking instead, if any.
func (u *Universe) EvaluateCheck(pkg, def string, lets map[string]any, expr string, instance map[string]any) (Result, []string) {
	return u.EvaluateCheckWith(pkg, def, lets, expr, instance, nil)
}

// EvaluateCheckWith is EvaluateCheck on an instance some of whose lists hold possible members:
// possible gives, by field, the indexes of the items of that list a log would list as possible
// members, because their facts are pending.
func (u *Universe) EvaluateCheckWith(pkg, def string, lets map[string]any, expr string, instance map[string]any, possible map[string][]int) (Result, []string) {
	p := u.Packages[pkg]
	if p == nil || p.Defs[def] == nil {
		return Result{}, []string{fmt.Sprintf("no definition %s/%s", pkg, def)}
	}
	before := len(u.errs)
	c, err := u.checkContext(p, p.Defs[def])
	if err != nil {
		return Result{}, []string{err.Error()}
	}
	var raw any
	if len(lets) > 0 {
		m := map[string]any{}
		for k, v := range lets {
			m[k] = v
		}
		raw = m
	}
	c, letAsts := u.lets(c, raw, "vector.let")
	a := u.compile(c, expr, "vector.expr", types.BoolType)
	if errs := u.errs[before:]; len(errs) > 0 || a == nil {
		problems := append([]string(nil), errs...)
		u.errs = u.errs[:before]
		if len(problems) == 0 {
			problems = []string{"does not compile"}
		}
		return Result{}, problems
	}
	if len(possible) > 0 {
		marked := make(map[string]any, len(instance))
		for k, v := range instance {
			marked[k] = v
		}
		for _, field := range sortedKeys(possible) {
			items, isList := instance[field].([]any)
			if !isList {
				return Result{}, []string{fmt.Sprintf("possible: %s is not a list of the instance", field)}
			}
			list, err := PossibleList(items, possible[field])
			if err != nil {
				return Result{}, []string{fmt.Sprintf("possible: %s: %v", field, err)}
			}
			marked[field] = list
		}
		instance = marked
	}
	act := &lazy{self: instance, programs: map[string]cel.Program{}, memo: map[string]ref.Val{}}
	for _, l := range letAsts {
		prg, err := u.program(c, l.ast)
		if err != nil {
			return Result{}, []string{err.Error()}
		}
		act.programs[l.name] = prg
	}
	prg, err := u.program(c, a)
	if err != nil {
		return Result{}, []string{err.Error()}
	}
	return result(prg.Eval(act)), nil
}

// program plans an expression for evaluation, with && and || evaluating both operands
// (strictOperands) and == and != reading lists with possible members (equalityDecorator).
func (u *Universe) program(c *context, a *cel.Ast) (cel.Program, error) {
	strict, err := strictOperands(c.env, a)
	if err != nil {
		return nil, err
	}
	return c.env.Program(strict, equalityDecorator())
}

func result(out ref.Val, _ *cel.EvalDetails, err error) Result {
	if err != nil {
		return Result{"unknown", err.Error()}
	}
	switch v := out.(type) {
	case types.Bool:
		if v {
			return Result{Value: "true"}
		}
		return Result{Value: "false"}
	case *types.Err:
		return Result{"unknown", v.Error()}
	case *types.Unknown:
		return Result{"unknown", reasonOf(v)}
	}
	return Result{"unknown", fmt.Sprintf("a %s, not a boolean", out.Type().TypeName())}
}

// lazy binds self and the lets: a let is evaluated only when read, at most once, and if it is
// unknown, only what reads it is unknown.
type lazy struct {
	self     map[string]any
	programs map[string]cel.Program
	memo     map[string]ref.Val
}

func (a *lazy) ResolveName(name string) (any, bool) {
	if name == "self" {
		return a.self, a.self != nil
	}
	prg, ok := a.programs[name]
	if !ok {
		return nil, false
	}
	if v, done := a.memo[name]; done {
		return v, true
	}
	v, _, err := prg.Eval(a)
	if err != nil {
		v = types.WrapErr(err)
	}
	a.memo[name] = v
	return v, true
}

func (a *lazy) Parent() interpreter.Activation { return nil }
