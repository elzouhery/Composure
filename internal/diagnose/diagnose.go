// Package diagnose runs a resolved compose project and its topology past a set
// of rules and reports what is wrong with it.
//
// Three constraints shape everything here.
//
// AD-7: every Finding carries at least one Anchor — a {file, line, column}
// position in the source. A rule that cannot anchor its finding is not
// shipped, because "port 5432 collides" without two positions is a sentence
// nobody can act on in a merged configuration.
//
// AD-13: findings are not errors and the two never cross. A file that will not
// parse produces a typed error from resolve and no model at all; a file that
// resolves but is wrong produces a model plus findings. Run returns an error
// only when it was handed nothing to work with.
//
// AD-17: a rule that needs a relationship reads internal/topology. It does not
// re-walk depends_on, re-pair services onto networks, or re-derive published
// ports from the resolved model — a second implementation of a relation is a
// second implementation that silently diverges. Only rules purely local to one
// value (credentials in `environment:`) read the resolved model alone.
//
// Nothing imports this package. It sits above resolve and topology and below
// the CLI and the RPC surface.
//
// Phase 1 writes nothing. A Fix is data describing an edit — an operation, a
// Path, and the byte range in the file it would touch, derived through
// strategy's own locate path so that the range reported here is the range
// story 6.1 later splices. Computing it is what proves phase 2 can apply it.
package diagnose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Severity ranks a finding. It is the severity of a *finding*, never a Go
// error: SeverityError means "this stack will not work", not "diagnose
// failed". AD-13 keeps those two channels apart, and the exit code of `composure
// diagnose` reads this and nothing else.
type Severity int

const (
	// SeverityHint is advice. It never blocks anything and it is never a
	// reason to fail a build on its own — story 3.1's credential rule is
	// deliberately here rather than one step up.
	SeverityHint Severity = iota
	// SeverityWarning is a real defect that the stack may nonetheless survive.
	SeverityWarning
	// SeverityError is a configuration that cannot work as written: a startup
	// that can never complete, a port that cannot bind.
	SeverityError
)

func (s Severity) String() string {
	switch s {
	case SeverityHint:
		return "hint"
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	}
	return "unknown"
}

// MarshalJSON emits the name rather than the ordinal. A consumer reading
// `"severity": 2` has to keep a copy of this enum in sync; one reading
// `"severity": "error"` does not.
func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Severity) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	v, ok := ParseSeverity(name)
	if !ok {
		return fmt.Errorf("diagnose: unknown severity %q", name)
	}
	*s = v
	return nil
}

// ParseSeverity turns a severity name back into a value.
func ParseSeverity(name string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "hint":
		return SeverityHint, true
	case "warning", "warn":
		return SeverityWarning, true
	case "error":
		return SeverityError, true
	}
	return 0, false
}

// Anchor is one provenance reference on a finding: where in which file the
// thing being complained about was written, and what role that position plays.
//
// Path is carried alongside Origin because the two answer different questions.
// Origin says where to put the cursor; Path says what node it is, which is what
// joins a finding to a graph node and to a byte range (AD-14).
type Anchor struct {
	// Label says what this position is — "declared here", "depends_on",
	// "published here". A finding with three anchors is unreadable without it.
	Label  string         `json:"label"`
	Path   resolve.Path   `json:"-"`
	Origin resolve.Origin `json:"origin"`
}

// MarshalJSON renders Path in its one canonical string form (AD-14).
func (a Anchor) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Label  string         `json:"label"`
		Path   string         `json:"path"`
		Origin resolve.Origin `json:"origin"`
	}{a.Label, a.Path.String(), a.Origin})
}

// ByteRange is a half-open span of a file, [Start, End). An insertion has
// Start == End: it touches no existing byte.
type ByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
	// Line is the 1-based line the range begins on, so a reader who has the
	// file open does not have to count bytes.
	Line int `json:"line"`
}

// Fix operations. Each names something the splice engine can already do, so a
// described fix is a request phase 2 can execute rather than prose it has to
// interpret.
const (
	// FixReplaceScalar overwrites the scalar at Path with Value.
	FixReplaceScalar = "replace_scalar"
	// FixDeleteKey removes the key at Path with its subtree and attached
	// comments — strategy.DeleteKey, whose exact range Fix.Range reports.
	FixDeleteKey = "delete_key"
	// FixInsertKey adds `Key: Value` as a child of the mapping at Path.
	FixInsertKey = "insert_key"
)

// Remedy operations. The set is closed for the same reason the fix set is: a
// consumer must be able to execute the remedy rather than interpret prose.
const (
	// RemedyExtract is `composure extract` — story 9.3's move of a literal into a
	// variable, which finishes the splice half by putting the secret somewhere.
	RemedyExtract = "extract"
)

// Remedy is the part of a fix that is NOT a single-file splice — story 9.5,
// DECISIONS.md 26.
//
// It exists because the plaintext-credential rule could only ever describe half
// of its own advice. `replace_scalar` to `${POSTGRES_PASSWORD}` is a correct
// edit and an incomplete remedy: it leaves the reader with a compose file
// referencing a variable nothing defines, which `docker compose` resolves to
// the empty string without a word. The other half is one line in a `.env`, and
// `Fix` had nowhere to put it.
//
// WHAT IT DELIBERATELY DOES NOT HAVE IS A BYTE RANGE, and that is the design
// rather than an omission. `Fix.Range` exists so `disowned` can check that a
// described edit owns the bytes it names, using the finding's own line as an
// independent oracle. There is no such oracle here: the `.env` line does not
// exist until it is written, its file is not one the report was built from, and
// its offset is not knowable from anything in Sources. A Range that could not
// be checked would be exactly the unverifiable claim `disowned` was written to
// stop, so this carries none — UNREPRESENTABLE rather than unchecked.
//
// The splice half is unaffected: the Fix this hangs off still goes through
// unownedFix and disowned, unchanged.
//
// It is DATA, like every other part of a Fix. Offering it stages nothing and
// performs nothing (DECISIONS.md 17): the reader still chooses, and the
// operation named here re-derives everything and refuses on its own terms.
type Remedy struct {
	// Operation is one of the Remedy* constants above.
	Operation string `json:"operation"`
	// At is the config path the operation takes. It is the SAME address the fix
	// names — that is the whole point of the field, and DECISIONS.md 25 recorded
	// the coincidence a year before there was a field to record it in.
	At string `json:"at"`
	// Name is the variable the operation would define, and it is the name the
	// fix's own `${NAME}` uses. The two cannot be allowed to drift, so the
	// Command below passes it explicitly rather than letting the operation
	// derive its own.
	Name string `json:"name"`
	// File is the second file the operation would write — the `.env` beside the
	// compose file, which is the only one Compose interpolates from.
	File string `json:"file"`
	// Command is the exact headless invocation. CLI before UI, always: the
	// remedy a pane offers and the remedy a terminal runs are one thing.
	Command string `json:"command"`
	// Describe is one sentence a human can read instead of the operation name.
	Describe string `json:"describe"`
}

// Fix describes an edit that would resolve a finding. It is data, never an
// action: phase 1 has no write path at all.
//
// Range is derived through strategy's locate path from the same resolve.Path
// the fix names, so the bytes reported here are the bytes phase 2 would splice
// (AD-14). A fix whose range cannot be derived is not offered — see
// Finding.NoFix.
//
// Path addresses File, not the merged model. A finding's Anchors carry merged
// paths because that is what a finding is about; a fix carries the path the node
// has in the one file it would edit, because a merged sequence index does not
// address the same element there. See localpath.go.
type Fix struct {
	Operation string       `json:"operation"`
	Path      resolve.Path `json:"-"`
	File      string       `json:"file"`
	Range     ByteRange    `json:"range"`
	// Key and Value carry the operation's argument, empty where it has none.
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	// NeedsValue says that Value is deliberately absent and the author has to
	// supply one: the fix locates the edit, it does not decide it.
	//
	// It exists because an empty Value is otherwise indistinguishable from "set
	// this to the empty string". A consumer that applied the port-collision fix
	// verbatim would write `- ""` into a ports list and report success: valid
	// YAML, invalid compose. Run refuses to emit a value-writing fix that leaves
	// Value empty without setting this.
	NeedsValue bool `json:"needs_value,omitempty"`
	// Describe is one sentence a human can read instead of the operation name.
	Describe string `json:"describe"`
	// Remedy is the half of the advice that is not a splice in this file, or
	// nil where the whole remedy is the splice. See Remedy's own comment for why
	// it carries no byte range and why that is the design.
	Remedy *Remedy `json:"remedy,omitempty"`
}

// MarshalJSON renders Path in its canonical string form.
func (f Fix) MarshalJSON() ([]byte, error) {
	type alias Fix
	return json.Marshal(struct {
		alias
		Path string `json:"path"`
	}{alias(f), f.Path.String()})
}

// Finding is one thing a rule found. Anchors is never empty: a finding that
// cannot say where it is does not exist (AD-7), and the rules enforce that by
// declining to emit rather than by emitting something positionless.
type Finding struct {
	// Rule is the stable id of the rule that produced it, e.g.
	// "credential-in-environment". It is what a suppression list would key on.
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	// Title is the rule's short label, the same for every finding of a rule.
	Title string `json:"title"`
	// Message is this instance, in one sentence. It never contains a secret
	// value: printing the password you just found in a file is how a
	// diagnostic ends up in a CI log.
	Message string `json:"message"`
	// Subjects are the nodes the finding is about — a service, a volume, the
	// two ends of a collision. Display and joining; the positions are Anchors.
	Subjects []resolve.Path `json:"-"`
	Anchors  []Anchor       `json:"anchors"`
	// Fix is the described edit, or nil when none is offered.
	Fix *Fix `json:"fix,omitempty"`
	// NoFix says why no fix is offered, and is set exactly when Fix is nil.
	// "Which link of a cycle to break is a judgement call" is a better answer
	// than silence, and it is the answer story 3.8 asks for.
	NoFix string `json:"no_fix,omitempty"`
	// NoRemedy says why a remedy a rule OFFERED was dropped by Run. It is set
	// only in that case — never on the great majority of findings, for which no
	// remedy was ever proposed and "there is no remedy" is not news. Rule 6:
	// a guard that silently discards is a guard nobody can debug.
	NoRemedy string `json:"no_remedy,omitempty"`
}

// MarshalJSON renders Subjects in their canonical string form and guarantees
// the lists are present rather than null.
func (f Finding) MarshalJSON() ([]byte, error) {
	subjects := make([]string, 0, len(f.Subjects))
	for _, s := range f.Subjects {
		subjects = append(subjects, s.String())
	}
	anchors := f.Anchors
	if anchors == nil {
		anchors = []Anchor{}
	}
	type alias Finding
	return json.Marshal(struct {
		alias
		Subjects []string `json:"subjects"`
		Anchors  []Anchor `json:"anchors"`
	}{alias(f), subjects, anchors})
}

// Report is the whole result of a run: what was diagnosed, under which
// profiles, and what was found.
type Report struct {
	// Path is the entry file as the caller named it.
	Path string `json:"path"`
	// Profiles is the active profile set the topology was built for.
	Profiles []string  `json:"profiles"`
	Findings []Finding `json:"findings"`
	// Rules is every rule that ran, in registration order, so a consumer can
	// tell "the rule found nothing" from "the rule does not exist here".
	Rules []RuleInfo `json:"rules"`
}

// RuleInfo describes a registered rule.
type RuleInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Provisional marks a rule whose input is not yet fully available, so a
	// consumer knows its silence is not evidence. See rule_undefined_var.go.
	Provisional bool `json:"provisional,omitempty"`
}

// Highest returns the highest severity present, and whether there was one.
func (r *Report) Highest() (Severity, bool) {
	if len(r.Findings) == 0 {
		return 0, false
	}
	worst := r.Findings[0].Severity
	for _, f := range r.Findings[1:] {
		if f.Severity > worst {
			worst = f.Severity
		}
	}
	return worst, true
}

// Counts returns the number of findings per rule id.
func (r *Report) Counts() map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		out[f.Rule]++
	}
	return out
}

// Input is everything the rules read. It is a struct rather than a list of
// arguments so that adding a rule which needs a new input does not change every
// call site.
type Input struct {
	// Path is the entry file, for the report header only.
	Path string
	// Project is the resolved model. Never nil.
	Project *resolve.Project
	// Graph is the topology under the active profile set. Never nil.
	Graph *topology.Graph
	// AllProfiles is the topology with every profile the project declares
	// active. It exists for exactly one question — "is this volume used by a
	// service that some other profile would start" — which the active graph
	// cannot answer because a filtered service contributes no edges. It is
	// still topology's edges, not a second derivation (AD-17). Optional; a nil
	// here narrows rule 3.5 rather than breaking it.
	AllProfiles *topology.Graph
	// Sources maps a file path, as it appears in Origin.File, to its bytes.
	// Fixes need them to compute a byte range. A file missing here costs its
	// findings their fix, never their existence.
	Sources map[string][]byte
	// Env is the variable environment interpolation would draw on: the host
	// environment merged under any project .env. Provisional — see
	// rule_undefined_var.go.
	Env map[string]string
	// EnvKnown reports whether Env was actually populated. A nil Env and an
	// empty one are different claims, and the difference decides whether the
	// undefined-variable rule may speak at all.
	EnvKnown bool
}

// context is Input plus the small amount of shared machinery the rules use. It
// is unexported: a rule is internal to this package by construction, which is
// what keeps the registry the only way one can be added.
type context struct {
	Input
}

// Rule is one check. Implementations live one to a file and are registered in
// the slice in registry.go — adding a rule is adding a file and one line, never
// editing a switch.
type rule interface {
	id() string
	title() string
	apply(*context) []Finding
}

// provisionalRule is implemented by a rule whose input is not yet fully
// available. It is a separate interface so that the common case stays a
// two-method contract.
type provisionalRule interface {
	provisional() bool
}

// Run applies every registered rule and returns the report.
//
// It returns an error only when it was handed nothing to diagnose. A wrong
// configuration is findings; a missing model is an error (AD-13).
func Run(in Input) (*Report, error) {
	if in.Project == nil {
		return nil, ErrNoProject
	}
	if in.Graph == nil {
		return nil, ErrNoGraph
	}
	c := &context{Input: in}

	rep := &Report{
		Path:     in.Path,
		Profiles: in.Graph.Profiles(),
		Findings: []Finding{},
		Rules:    make([]RuleInfo, 0, len(rules)),
	}
	if rep.Profiles == nil {
		rep.Profiles = []string{}
	}
	for _, r := range rules {
		info := RuleInfo{ID: r.id(), Title: r.title()}
		if p, ok := r.(provisionalRule); ok {
			info.Provisional = p.provisional()
		}
		rep.Rules = append(rep.Rules, info)

		for _, f := range r.apply(c) {
			// AD-7, enforced here rather than trusted: a finding with no
			// anchor is dropped and the drop is a bug in the rule, not a
			// silent degradation of the report.
			if len(f.Anchors) == 0 {
				continue
			}
			if f.Rule == "" {
				f.Rule = r.id()
			}
			if f.Title == "" {
				f.Title = r.title()
			}
			// A fix that writes a value must carry one or say that the author
			// has to. Enforced here rather than trusted per rule: a consumer
			// cannot tell an omission from an instruction to write "".
			if why := unusableFix(f.Fix); why != "" {
				f.Fix, f.NoFix = nil, why
			}
			// ...and a fix must own the bytes it names. This was enforced only
			// inside the locateFix helper, which a rule building a Fix literal
			// simply never called — so a range on the wrong line, or one past
			// the end of the file, shipped unchallenged. Enforced here it is
			// construction rather than convention (rule 6: refuse, do not
			// describe an edit against bytes that are not there).
			if why := c.unownedFix(f.Fix); why != "" {
				f.Fix, f.NoFix = nil, why
			}
			// ...and a remedy must be one a consumer can execute against the
			// same address the fix names (story 9.5). It is dropped alone: the
			// splice half is still correct advice, and taking it away because
			// its companion was malformed would make the report worse than it
			// was before the field existed.
			if why := unusableRemedy(f.Fix); why != "" {
				f.Fix.Remedy, f.NoRemedy = nil, why
			}
			if f.Fix == nil && f.NoFix == "" {
				f.NoFix = "no mechanical fix is described for this rule"
			}
			rep.Findings = append(rep.Findings, f)
		}
	}
	sortFindings(rep.Findings)
	return rep, nil
}

// unusableFix reports why a fix must not be offered, or "".
//
// The one rule it enforces is that an operation which writes a value either
// carries the value or declares that the author has to supply it. Applying a
// replace_scalar with an empty Value writes an empty scalar and reports success:
// `- ""` in a ports list is valid YAML and invalid compose, and the failure
// surfaces later in someone else's terminal.
func unusableFix(f *Fix) string {
	if f == nil {
		return ""
	}
	if f.NeedsValue || f.Value != "" {
		return ""
	}
	switch f.Operation {
	case FixReplaceScalar, FixInsertKey:
		return fmt.Sprintf("no fix is described: the %s fix for this finding carries no value and does not declare that one is "+
			"needed, and applying it verbatim would write an empty value rather than the intended one", f.Operation)
	}
	return ""
}

// unusableRemedy reports why a remedy must not be offered, or "".
//
// Story 9.5, and it sits in Run beside unusableFix and unownedFix for the
// reason fix.go gives at length: a helper is something a rule can decline to
// call, and the last invariant that lived only inside a helper shipped a fix on
// the wrong line and one running past the end of the file. A rule cannot get
// past this, because Run runs it for every finding.
//
// It cannot check what disowned checks — there is no byte range here, and
// Remedy has no field for one — so what it checks instead is that every field a
// consumer would act on is present, and that the remedy is attached to the one
// operation it can actually complete. `composure extract` replaces a scalar and
// puts the literal in a `.env`; hanging it off a delete_key would advertise a
// move of something that is about to be removed.
func unusableRemedy(f *Fix) string {
	if f == nil || f.Remedy == nil {
		return ""
	}
	r := f.Remedy
	switch {
	case r.Operation != RemedyExtract:
		return fmt.Sprintf("no remedy is described: %q is not an operation this product performs, and a remedy a "+
			"consumer cannot execute is prose pretending to be data", r.Operation)
	case strings.TrimSpace(r.Name) == "":
		return "no remedy is described: it names no variable, so the value it moves would arrive somewhere nothing references"
	case strings.TrimSpace(r.File) == "":
		return "no remedy is described: it names no file to write the value to"
	case strings.TrimSpace(r.At) == "":
		return "no remedy is described: it names no config path, so it is not the same address as the finding"
	case strings.TrimSpace(r.Command) == "":
		return "no remedy is described: it carries no command, and CLI before UI means the offer and the invocation are one thing"
	case f.Operation != FixReplaceScalar:
		return fmt.Sprintf("no remedy is described: the extract remedy completes a replace_scalar and this fix is a %s, "+
			"so it would advertise moving a value the fix is about to remove", f.Operation)
	case r.At != f.Path.String():
		return fmt.Sprintf("no remedy is described: the remedy addresses %s and the fix addresses %s. The whole value of "+
			"this field is that the finding and the remedy are ONE address; two is worse than none", r.At, f.Path)
	}
	return ""
}

// sortFindings puts the report in a deterministic, readable order: worst first,
// then by rule, then by position. Two runs over the same project must produce
// byte-identical JSON, or the tool cannot be diffed in CI.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		ao, bo := firstOrigin(a), firstOrigin(b)
		if ao.File != bo.File {
			return ao.File < bo.File
		}
		if ao.Line != bo.Line {
			return ao.Line < bo.Line
		}
		if ao.Column != bo.Column {
			return ao.Column < bo.Column
		}
		return a.Message < b.Message
	})
}

// firstOrigin is the position a finding sorts on. Run drops anchorless findings
// (AD-7), so this is never reached with one — but sortFindings is a plain
// function over a slice, and a panic on the day someone calls it with a hand-made
// Finding would be a poor way to learn that.
func firstOrigin(f Finding) resolve.Origin {
	if len(f.Anchors) == 0 {
		return resolve.Origin{}
	}
	return f.Anchors[0].Origin
}

// Rules lists the registered rules, for `composure diagnose` to print and for a
// consumer to discover what ran.
func Rules() []RuleInfo {
	out := make([]RuleInfo, 0, len(rules))
	for _, r := range rules {
		info := RuleInfo{ID: r.id(), Title: r.title()}
		if p, ok := r.(provisionalRule); ok {
			info.Provisional = p.provisional()
		}
		out = append(out, info)
	}
	return out
}
