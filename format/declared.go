package format

import (
	"fmt"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
)

// DeclaredKinds returns the kinds, as package/definition, that a use takes as declared: those its
// accepts_declared key lists. IndependentKinds returns those its independent key requires as an
// independent decision. Both read the key from the expression itself and never guess it from its
// words. It is a list of string literals, a conditional whose two branches are such lists, or
// the name of a let of the use that is either, and the kinds are those of every branch, so a kind
// that only one case adds is listed. A key of any other form has no list of kinds that can be read
// without evaluating it, and the function returns an error that says so. A use with no key, or
// whose key lists none, returns no kinds. The use is a use of the expanded content of a
// definitions file, which holds the lets its included parts add.
func DeclaredKinds(use map[string]any) ([]string, error) {
	return useKinds(use, "accepts_declared")
}

// IndependentKinds returns the kinds, as package/definition, that a use requires as an
// independent decision: those its independent key lists, read as DeclaredKinds reads its key.
func IndependentKinds(use map[string]any) ([]string, error) {
	return useKinds(use, "independent")
}

func useKinds(use map[string]any, key string) ([]string, error) {
	raw, has := use[key]
	if !has {
		return nil, nil
	}
	expr, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%s is not an expression", key)
	}
	lets := mapping(use["let"])
	found := map[string]bool{}
	if err := declaredKinds(expr, lets, map[string]bool{}, found); err != nil {
		return nil, err
	}
	return sortedKeys(found), nil
}

func declaredKinds(expr string, lets map[string]any, visiting map[string]bool, found map[string]bool) error {
	env, err := cel.NewEnv()
	if err != nil {
		return err
	}
	parsed, iss := env.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return fmt.Errorf("%q does not parse: %w", expr, iss.Err())
	}
	return declaredKindsOf(parsed.NativeRep().Expr(), lets, visiting, found)
}

func declaredKindsOf(e celast.Expr, lets map[string]any, visiting map[string]bool, found map[string]bool) error {
	switch {
	case e.Kind() == celast.ListKind:
		for _, el := range e.AsList().Elements() {
			kind, ok := stringLiteral(el)
			if !ok {
				return fmt.Errorf("a list of kinds holds an element that is not a string literal, so the kinds it takes cannot be read without evaluating it")
			}
			found[kind] = true
		}
		return nil
	case e.Kind() == celast.CallKind && e.AsCall().FunctionName() == "_?_:_":
		args := e.AsCall().Args()
		for _, branch := range args[1:] {
			if err := declaredKindsOf(branch, lets, visiting, found); err != nil {
				return err
			}
		}
		return nil
	case e.Kind() == celast.IdentKind:
		name := e.AsIdent()
		body, isLet := lets[name].(string)
		switch {
		case !isLet:
			return fmt.Errorf("%s is not a let of the use, so the kinds it names cannot be read", name)
		case visiting[name]:
			return fmt.Errorf("let %s refers to itself", name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		return declaredKinds(body, lets, visiting, found)
	}
	return fmt.Errorf("it is neither a list of string literals, nor a conditional of such lists, nor the name of a let that is one, so the kinds it takes cannot be read without evaluating it")
}
