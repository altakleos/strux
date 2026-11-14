package format

import (
	"fmt"
	"sort"
	"strings"
)

// An abstract operation is declared by one package and filled by another: the declaring package
// says where the operation runs, what it takes and what it returns, refuses and reports; a
// binder in a package that depends on it supplies the effect. A version lists exactly one binder
// for each abstract operation that a package it lists performs.

// bindsCheck checks one operation's binds key against the abstract operation it names.
func (u *Universe) bindsCheck(p *Package, d *Definition, oname string, om map[string]any) {
	target, ok := om["binds"].(string)
	if !ok {
		return
	}
	where := d.Name + ".operations." + oname + ".binds"
	if !strings.Contains(target, "/") {
		u.errorf(p, where, "%s must name an operation of a package this one depends on, as package/definition.operations.name", target)
		return
	}
	def, parts, node, err := u.Element(p, target, false)
	if err != nil {
		u.errorf(p, where, "%v", err)
		return
	}
	if Shape(parts) != "operation" {
		u.errorf(p, where, "%s is not an operation", target)
		return
	}
	abs := mapping(node)
	if abs["abstract"] != true {
		u.errorf(p, where, "%s is not abstract, so nothing binds it", target)
		return
	}
	if a, b := u.pointOf(def.Pkg, abs), u.pointOf(p, om); a != b {
		u.errorf(p, where, "%s runs at %s, but the binder runs at %s", target, a, b)
	}
	bind := d.Inputs[oname]
	for name, f := range def.Inputs[parts[len(parts)-1]].fieldsOrNil() {
		got := bind.fieldsOrNil()[name]
		switch {
		case got == nil:
			u.errorf(p, where, "the binder takes no input %s, which %s takes", name, target)
		case !sameType(f.Type, got.Type):
			u.errorf(p, where, "input %s has type %s, but %s takes %s", name, got.TypeSrc, target, f.TypeSrc)
		}
		if got != nil && got.Required && !f.Required {
			u.errorf(p, where, "input %s is required by the binder but optional in %s, so a caller that follows %s could be refused", name, target, target)
		}
	}
	have := def.Inputs[parts[len(parts)-1]].fieldsOrNil()
	for _, name := range sortedKeys(bind.fieldsOrNil()) {
		if got := bind.fieldsOrNil()[name]; have[name] == nil && got.Required {
			u.errorf(p, where, "input %s is required by the binder but not taken by %s, so a caller that follows %s would be refused", name, target, target)
		}
	}
}

func (d *Decl) fieldsOrNil() map[string]*Field {
	if d == nil {
		return nil
	}
	return d.Fields
}

func (u *Universe) pointOf(p *Package, om map[string]any) string {
	at := fmt.Sprint(om["at"])
	if resolved, err := u.Point(p, at); err == nil {
		return resolved
	}
	return at
}

// sameType reports whether two resolved types are the same type.
func sameType(a, b *Type) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case "scalar":
		return a.Name == b.Name
	case "named":
		return a.Decl == b.Decl
	case "list":
		return sameType(a.Elem, b.Elem)
	case "map":
		return sameType(a.Key, b.Key) && sameType(a.Elem, b.Elem)
	case "enum":
		return strings.Join(a.Values, ",") == strings.Join(b.Values, ",")
	case "ref":
		if len(a.Target) != len(b.Target) {
			return false
		}
		for i := range a.Target {
			if a.Target[i] != b.Target[i] {
				return false
			}
		}
		return strings.Join(a.Refs, "|") == strings.Join(b.Refs, "|")
	}
	return false
}

// AbstractOperation names an abstract operation, as package/definition.operations.name.
type AbstractOperation string

// Binders maps each abstract operation among pkgs to the operations that bind it.
func Binders(pkgs map[string]*Package) map[AbstractOperation][]string {
	out := map[AbstractOperation][]string{}
	for _, pn := range sortedKeys(pkgs) {
		p := pkgs[pn]
		for _, dn := range sortedKeys(p.Defs) {
			ops := mapping(p.Defs[dn].Raw["operations"])
			for _, on := range sortedKeys(ops) {
				om := mapping(ops[on])
				if om["abstract"] == true {
					key := AbstractOperation(pn + "/" + dn + ".operations." + on)
					if _, ok := out[key]; !ok {
						out[key] = nil
					}
				}
				if b, ok := om["binds"].(string); ok {
					out[AbstractOperation(b)] = append(out[AbstractOperation(b)], pn+"/"+dn+".operations."+on)
				}
			}
		}
	}
	return out
}

// CheckBinders checks a version's package list: each abstract operation that a listed package
// performs has exactly one listed binder. listed names the packages the version takes.
func CheckBinders(pkgs map[string]*Package, listed []string) []string {
	in := map[string]bool{}
	for _, n := range listed {
		in[n] = true
	}
	all := Binders(pkgs)
	var errs []string
	performed := map[AbstractOperation][]string{}
	for _, pn := range sortedKeys(pkgs) {
		if !in[pn] {
			continue
		}
		p := pkgs[pn]
		for _, dn := range sortedKeys(p.Defs) {
			ops := mapping(p.Defs[dn].Raw["operations"])
			for _, on := range sortedKeys(ops) {
				for _, e := range list(mapping(ops[on])["performs"]) {
					path, _ := e.(string)
					if !strings.Contains(path, "/") {
						path = pn + "/" + path
					}
					if _, abstract := all[AbstractOperation(path)]; abstract {
						performed[AbstractOperation(path)] = append(performed[AbstractOperation(path)], pn+"/"+dn+".operations."+on)
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(performed))
	for k := range performed {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		var bound []string
		for _, b := range all[AbstractOperation(k)] {
			if in[strings.SplitN(b, "/", 2)[0]] {
				bound = append(bound, b)
			}
		}
		switch len(bound) {
		case 1:
		case 0:
			errs = append(errs, fmt.Sprintf("%s is abstract and %s performs it, but the version lists no package that binds it", k, strings.Join(performed[AbstractOperation(k)], ", ")))
		default:
			errs = append(errs, fmt.Sprintf("%s is abstract and the version lists %d binders: %s", k, len(bound), strings.Join(bound, ", ")))
		}
	}
	return errs
}
