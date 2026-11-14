package format

// Parts. A package may declare
// parts: templates of lets and conditions, in the shape of a use's, that a use includes. A part is
// not a use, so no grant names it and no fact is decided by it. The package is read as if each
// including use had written the lets and conditions of the parts it includes, so an included
// condition has the element path of a condition written in the use, a part is type-checked at each
// use that includes it, and the canonical form, which the digest is taken of, has no parts.

// partKeys are the keys a part may have.
var partKeys = []string{"let", "conditions"}

// Expanded returns the content of a definitions file with its parts expanded into the uses that
// include them: the canonical form that identifies the package. Content with no parts is returned
// as it is. A part or an include that is wrong is left out, because Build reports it.
func Expanded(content any) any {
	m, ok := content.(map[string]any)
	if !ok || m["package"] == nil || m["definitions"] == nil {
		return content // not a definitions file
	}
	scratch := &Package{File: "content"}
	return scratch.expand(m)
}

// expand reads the parts of a package's content and the includes of its uses, reports what is
// wrong with them, and returns the content with each use holding the lets and conditions of the
// parts it includes, and no parts and no includes.
func (p *Package) expand(m map[string]any) map[string]any {
	defs := mapping(m["definitions"])
	if m["parts"] == nil && !anyIncludes(defs) {
		return m
	}
	parts := map[string]map[string]any{}
	p.invalidParts = map[string]bool{}
	if raw, has := m["parts"]; has {
		all, isMap := raw.(map[string]any)
		if !isMap {
			p.errorf("parts", "must be a mapping")
		}
		for _, name := range sortedKeys(all) {
			if part, ok := p.part(name, all[name]); ok {
				parts[name] = part
			} else {
				p.invalidParts[name] = true
			}
		}
	}
	used := map[string]bool{}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != "parts" {
			out[k] = v
		}
	}
	if defs != nil {
		newDefs := make(map[string]any, len(defs))
		for _, dname := range sortedKeys(defs) {
			newDefs[dname] = p.expandDefinition(dname, defs[dname], parts, used)
		}
		out["definitions"] = newDefs
	}
	for _, name := range sortedKeys(parts) {
		if !used[name] {
			p.errorf("parts."+name, "part %s is included by no use, so nothing reads it", name)
		}
	}
	return out
}

func anyIncludes(defs map[string]any) bool {
	for _, d := range defs {
		for _, u := range mapping(mapping(d)["uses"]) {
			if _, has := mapping(u)["includes"]; has {
				return true
			}
		}
	}
	return false
}

// part checks the shape of one part. What its conditions say is checked where it is included.
func (p *Package) part(name string, raw any) (map[string]any, bool) {
	where := "parts." + name
	before := len(p.errs)
	p.name(where, name)
	pm, ok := raw.(map[string]any)
	if !ok {
		p.errorf(where, "must be a mapping")
		return nil, false
	}
	p.keys(where, pm, nil, partKeys...)
	if pm["let"] == nil && pm["conditions"] == nil {
		p.errorf(where, "part %s has neither let nor conditions; a part states lets, conditions or both", name)
	}
	p.lets(where+".let", pm)
	p.mapOf(where+".conditions", pm)
	for _, cname := range sortedKeys(mapping(pm["conditions"])) {
		p.name(where+".conditions."+cname, cname)
	}
	return pm, len(p.errs) == before
}

// expandDefinition expands the uses of one definition.
func (p *Package) expandDefinition(dname string, raw any, parts map[string]map[string]any, used map[string]bool) any {
	d, ok := raw.(map[string]any)
	uses := mapping(d["uses"])
	if !ok || uses == nil {
		return raw
	}
	newDef := make(map[string]any, len(d))
	for k, v := range d {
		newDef[k] = v
	}
	newUses := make(map[string]any, len(uses))
	for _, uname := range sortedKeys(uses) {
		newUses[uname] = p.expandUse(dname+".uses."+uname, uses[uname], parts, used)
	}
	newDef["uses"] = newUses
	return newDef
}

// expandUse adds the lets and conditions of the parts a use includes to the use, and takes
// includes out. A name that the use and its parts, or two parts, take twice is an error.
func (p *Package) expandUse(where string, raw any, parts map[string]map[string]any, used map[string]bool) any {
	um, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	includes, has := um["includes"]
	if !has {
		return raw
	}
	out := make(map[string]any, len(um))
	for k, v := range um {
		if k != "includes" {
			out[k] = v
		}
	}
	names, isList := includes.([]any)
	if !isList || len(names) == 0 {
		p.errorf(where+".includes", "must be a list of part names, and not empty")
		return out
	}
	lets, conditions := cloneMap(mapping(um["let"])), cloneMap(mapping(um["requires"]))
	takenLets, takenConditions := map[string]string{}, map[string]string{}
	for name := range lets {
		takenLets[name] = "the use"
	}
	for name := range conditions {
		takenConditions[name] = "the use"
	}
	for _, s := range list(um["requires_satisfied"]) {
		if name, ok := s.(string); ok {
			takenConditions[name] = "the use"
		}
	}
	seen := map[string]bool{}
	for _, item := range names {
		name, isString := item.(string)
		switch {
		case !isString:
			p.errorf(where+".includes", "must be a list of part names, and %v is not a name", item)
			continue
		case seen[name]:
			p.errorf(where, "includes %s twice", name)
			continue
		}
		seen[name] = true
		part, exists := parts[name]
		if !exists {
			if !p.invalidParts[name] {
				p.errorf(where, "includes %s, which is no part of the package", name)
			}
			continue
		}
		used[name] = true
		by := "part " + name
		for _, lname := range sortedKeys(mapping(part["let"])) {
			if prior, taken := takenLets[lname]; taken {
				p.errorf(where, "let %s is taken twice, by %s and by %s", lname, prior, by)
				continue
			}
			takenLets[lname] = by
			lets[lname] = clone(mapping(part["let"])[lname])
		}
		for _, cname := range sortedKeys(mapping(part["conditions"])) {
			if prior, taken := takenConditions[cname]; taken {
				p.errorf(where, "condition %s is taken twice, by %s and by %s", cname, prior, by)
				continue
			}
			takenConditions[cname] = by
			conditions[cname] = clone(mapping(part["conditions"])[cname])
		}
	}
	// A use that writes let or requires as anything but a mapping keeps what it wrote, so that
	// reading the expanded use reports it as it would for a use that includes nothing.
	if _, written := um["let"]; len(lets) > 0 && (!written || mapping(um["let"]) != nil) {
		out["let"] = lets
	}
	if _, written := um["requires"]; len(conditions) > 0 && (!written || mapping(um["requires"]) != nil) {
		out["requires"] = conditions
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = clone(v)
	}
	return out
}

func clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = clone(e)
		}
		return out
	}
	return v
}
