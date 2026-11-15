package verify

import (
	"sort"

	"github.com/altakleos/strux/format"
)

// rootIn is a trust root of the chain and the position from which it is in force: the initial
// trust root from the log's start, and each recognized revision after its own position.
type rootIn struct {
	root     *TrustRoot
	from     int    // the position of the revision that made it, or -1 for the initial trust root
	identity string // what the next revision names as its predecessor
}

// rootAt is the trust root in force at a position.
func (r *run) rootAt(pos int) *TrustRoot {
	current := r.root
	for _, c := range r.chain {
		if c.from < pos {
			current = c.root
		}
	}
	return current
}

// signedBy reports whether as many distinct keys of a trust root as its threshold names signed the
// entry.
func signedBy(t *TrustRoot, e Entry) bool {
	distinct := map[string]bool{}
	for _, k := range e.Signers {
		if t.isKey(k) {
			distinct[k] = true
		}
	}
	return len(distinct) >= t.Threshold
}

// judgeRevision judges a trust-root revision at its position under the trust root in force there,
// by the kernel's own conditions, and records what it fails. It reads nothing but the revision,
// its fact and the trust root, and which revisions of the chain were recognized before it.
func (r *run) judgeRevision(e Entry, kernelUniverse *format.Universe) []string {
	p := e.Fact.Fact
	current := r.chain[len(r.chain)-1]
	var failed []string
	content, _ := r.content(e).(map[string]any)
	if p["package"] != r.kernel.digest {
		failed = append(failed, recognizeCondition("package-active"))
	}
	// signature: the trust-root keys in force, and as many keys of its own as its own threshold.
	ownSigned := signedByOwn(content, e)
	if p["key"] != nil || !signedBy(current.root, e) || !ownSigned {
		failed = append(failed, recognizeCondition("signature"))
	}
	decl := r.kernel.pkg.Defs["trust-root"]
	if content == nil || decl == nil || len(format.Conforms(decl.Decl, content)) > 0 {
		failed = append(failed, recognizeCondition("structure"))
		sort.Strings(failed)
		return failed
	}
	// trust-root-act: signed by the threshold of the trust root it supersedes and by its own,
	// naming that trust root, which no recognized revision has superseded first.
	var useFailed []string
	superseded := -1
	for i, c := range r.chain {
		if c.identity == str(p["expected"]) {
			superseded = i
		}
	}
	if superseded != len(r.chain)-1 || !signedBy(r.chain[superseded].root, e) || !ownSigned {
		useFailed = append(useFailed, kernelPackage+"/trust-root.uses.recognize.requires.trust-root-act")
	}
	results, err := kernelUniverse.EvaluateUse(kernelPackage, "trust-root", "recognize", format.UseInputs{Self: content, Fact: r.chainEnvelope(e), TrustRoot: current.root.Raw}, noReader{})
	if err != nil {
		useFailed = append(useFailed, kernelPackage+"/trust-root.uses.recognize")
	}
	for name, res := range results {
		if res.Value != "true" {
			useFailed = append(useFailed, kernelPackage+"/trust-root.uses.recognize.requires."+name)
		}
	}
	if len(useFailed) > 0 {
		failed = append(append(failed, recognizeCondition("conditions")), useFailed...)
	}
	sort.Strings(failed)
	return failed
}

// signedByOwn reports whether as many distinct keys of a revision's own as its threshold names
// signed it; a threshold of zero asks for none. Whether the revision is well formed is its kind's
// conditions to say.
func signedByOwn(content map[string]any, e Entry) bool {
	own := map[string]bool{}
	for _, k := range list(content["keys"]) {
		own[str(k)] = true
	}
	distinct := map[string]bool{}
	for _, k := range e.Signers {
		if own[k] {
			distinct[k] = true
		}
	}
	threshold, _ := content["threshold"].(int64)
	return int64(len(distinct)) >= threshold
}

// chainEnvelope is the envelope of a chain act's fact, as its conditions read it.
func (r *run) chainEnvelope(e Entry) map[string]any {
	p := e.Fact.Fact
	env := map[string]any{"kind": p["kind"], "log": p["log"], "package": p["package"], "digest": e.Fact.Identity, "instance": e.Fact.Identity, "revision_digest": p["content"], "position": int64(e.Position), "signer": r.rootAt(e.Position).Owner}
	for _, k := range []string{"expected", "operation", "operation_id", "append_by"} {
		if v, ok := p[k]; ok {
			env[k] = v
		}
	}
	return env
}

// noReader answers no read of the log: a chain act's conditions read only the revision, its fact
// and the trust root, which the checker holds them to.
type noReader struct{}

func (noReader) Resolve(format.Reference) (string, map[string]any, map[string]any, bool) {
	return "", nil, nil, false
}
func (noReader) History(string) ([]format.Reference, []int) { return nil, nil }
func (noReader) InForce(format.Reference) format.Result {
	return undecidedResult("a chain act reads no log")
}
func (noReader) Recognized(format.Reference) format.Result {
	return undecidedResult("a chain act reads no log")
}
func (noReader) Current(format.Reference) (format.Reference, format.Result) {
	return format.Reference{}, undecidedResult("a chain act reads no log")
}
func (noReader) Authorized(string, string) format.Result {
	return undecidedResult("a chain act reads no log")
}
func (noReader) OutsideReach(string, []string) format.Result {
	return undecidedResult("a chain act reads no log")
}
func (noReader) Holds(format.Reference, string) format.Result {
	return undecidedResult("a chain act reads no log")
}
func (noReader) Permitted(format.Reference, string) format.Result {
	return undecidedResult("a chain act reads no log")
}
func (noReader) Findings(string, string, bool) ([]format.Reference, []int) { return nil, nil }
func (noReader) Retained(string) format.Result                             { return undecidedResult("a chain act reads no log") }
func (noReader) Quoted(string, string, string) format.Result {
	return undecidedResult("a chain act reads no log")
}
