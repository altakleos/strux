package verify

import (
	"errors"
	"fmt"

	"github.com/altakleos/strux/format"
)

// kernelPackage is the package that interpretation revision 1 interprets as its kernel.
const kernelPackage = "kernel"

// TrustRoot is what verification reads of a trust root: its owner, its keys and how many of them
// must sign together, the log it names, the identities of the bootstrap facts in order, and the
// witnesses whose anchors count.
type TrustRoot struct {
	Raw       map[string]any // the trust root's content, as conditions read it
	Owner     string
	Keys      []string
	Threshold int
	Log       string
	Bootstrap []string
	Witnesses []Witness
}

// Witness is a principal whose anchors count, with the key it signs them with.
type Witness struct {
	Principal string
	Key       string
}

// ReadTrustRoot reads a trust root from its content.
func ReadTrustRoot(v any) (*TrustRoot, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("a trust root is an object")
	}
	t := &TrustRoot{Raw: m}
	t.Owner, _ = m["owner"].(string)
	t.Log, _ = m["log"].(string)
	threshold, _ := m["threshold"].(int64)
	t.Threshold = int(threshold)
	for _, k := range list(m["keys"]) {
		key, _ := k.(string)
		t.Keys = append(t.Keys, key)
	}
	for _, b := range list(m["bootstrap"]) {
		id, _ := b.(string)
		t.Bootstrap = append(t.Bootstrap, id)
	}
	for _, w := range list(m["witnesses"]) {
		wm, _ := w.(map[string]any)
		principal, _ := wm["principal"].(string)
		credential, _ := wm["credential"].(map[string]any)
		key, _ := credential["public_key"].(string)
		t.Witnesses = append(t.Witnesses, Witness{Principal: principal, Key: key})
	}
	if t.Owner == "" || t.Log == "" || len(t.Keys) == 0 || t.Threshold < 1 || len(t.Bootstrap) == 0 {
		return nil, fmt.Errorf("a trust root names its owner, log, keys, threshold and bootstrap facts")
	}
	return t, nil
}

func (t *TrustRoot) witness(key string) (string, bool) {
	for _, w := range t.Witnesses {
		if w.Key == key {
			return w.Principal, true
		}
	}
	return "", false
}

func (t *TrustRoot) isKey(key string) bool {
	for _, k := range t.Keys {
		if k == key {
			return true
		}
	}
	return false
}

// kernel is the revision of the kernel package that the bootstrap configuration pins, which judges
// the chain acts until the interpretation record pins one: its digest, and its definitions.
type kernel struct {
	digest string
	pkg    *format.Package
}

// bootstrapKernel finds the kernel that the bootstrap configuration pins: the configuration among
// the bootstrap facts names package revisions by exact reference, and the one of the kernel
// package records the digest of its definitions and captures their bytes.
func (r *run) bootstrapKernel() (*kernel, error) {
	bootstrap := map[string]Entry{}
	for i, id := range r.root.Bootstrap {
		if i >= len(r.entries) || r.entries[i].Fact == nil || r.entries[i].Fact.Identity != id {
			return nil, fmt.Errorf("the log does not begin with the bootstrap facts the trust root lists")
		}
		bootstrap[id] = r.entries[i]
	}
	for _, e := range bootstrap {
		if e.Fact.Fact["kind"] != kernelPackage+"/configuration" {
			continue
		}
		config, ok := r.content(e).(map[string]any)
		if !ok {
			return nil, errors.New("the bootstrap configuration's content cannot be read")
		}
		for _, p := range list(config["packages"]) {
			ref, _ := p.(map[string]any)
			id, _ := ref["fact"].(string)
			revision, ok := bootstrap[id]
			if !ok {
				continue
			}
			content, _ := r.content(revision).(map[string]any)
			if content["name"] != kernelPackage {
				continue
			}
			digest, _ := content["digest"].(string)
			bytesDigest, _ := content["bytes"].(string)
			defs, ok := r.log.Blobs[bytesDigest]
			if !ok {
				return nil, errors.New("the bootstrap's kernel revision carries no definitions")
			}
			parsed, err := format.Parse(defs)
			if err != nil {
				return nil, fmt.Errorf("the bootstrap's kernel definitions: %w", err)
			}
			pkg := format.Build(kernelPackage, "kernel definitions", parsed)
			if pkg.Digest != digest {
				return nil, errors.New("the bootstrap's kernel definitions do not have the digest its revision records")
			}
			if problems := format.Check(map[string]*format.Package{kernelPackage: pkg}, format.Kernel(kernelPackage)); len(problems) > 0 {
				return nil, fmt.Errorf("the bootstrap's kernel definitions do not conform: %s", problems[0])
			}
			return &kernel{digest: digest, pkg: pkg}, nil
		}
	}
	return nil, errors.New("the bootstrap configuration pins no kernel")
}

// content is an entry's content, as the canonical bytes it carries read it; nil where they are
// absent.
func (r *run) content(e Entry) any {
	if e.Fact == nil || e.Fact.ContentAbsent {
		return nil
	}
	digest, _ := e.Fact.Fact["content"].(string)
	v, err := format.ReadCanonical(r.log.Blobs[digest])
	if err != nil {
		return nil
	}
	return v
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// bootstrapPackages reads the package revisions the bootstrap configuration pins, checks them
// together and gives the universe their expressions evaluate in, with each package's name by the
// digest of its definitions, and the bootstrap's custody profile.
func (r *run) bootstrapPackages() (*format.Universe, map[string]string, error) {
	pkgs := map[string]*format.Package{}
	byDigest := map[string]string{}
	for i := range r.root.Bootstrap {
		e := r.entries[i]
		switch e.Fact.Fact["kind"] {
		case kernelPackage + "/custody-profile":
			r.bootstrapCustody, _ = r.content(e).(map[string]any)
		case kernelPackage + "/package-revision":
			content, _ := r.content(e).(map[string]any)
			name, _ := content["name"].(string)
			bytesDigest, _ := content["bytes"].(string)
			parsed, err := format.Parse(r.log.Blobs[bytesDigest])
			if err != nil {
				return nil, nil, fmt.Errorf("the bootstrap's %s definitions: %w", name, err)
			}
			p := format.Build(name, name, parsed)
			pkgs[name], byDigest[p.Digest] = p, name
		}
	}
	u, problems := format.Prepare(pkgs, format.Kernel(kernelPackage))
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("the bootstrap's packages do not conform: %s", problems[0])
	}
	return u, byDigest, nil
}
