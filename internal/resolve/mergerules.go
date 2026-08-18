package resolve

// Story 1.4, AD-4 — per-key merge behaviour is ONE declarative table keyed by
// config path, and the merge walker reads it.
//
// Adding a Compose key means adding a row below. It never means editing
// merge.go. That is the whole point: `ports` and `volumes` must not receive two
// different implementations of the same uniqueness rule, and the table has to
// stay diffable against compose-spec §13 by inspection — which is why every row
// carries the spec clause it encodes rather than a bare constant.
//
// The table is consulted by stories 1.4 (override file), 1.6 (`-f` chain), 1.7
// (`include:`) and 1.8 (`extends:`). There is exactly one merge implementation
// underneath all four.

import "strings"

// behaviour is what the walker does when a key appears in both sides.
type behaviour uint8

const (
	// behaviourDefault is compose-spec §13's default: mappings merge key-wise
	// and sequences append. It is what an unlisted path gets.
	behaviourDefault behaviour = iota
	// behaviourReplace: the later declaration wins outright. A sequence that
	// appended here would produce a command line nobody wrote.
	behaviourReplace
	// behaviourAppendUnique: a sequence whose elements name a resource. An
	// element whose key matches an existing one merges into it rather than
	// being appended beside it, which is how `- "8080:80"` in an override file
	// changes a port instead of publishing a second one.
	behaviourAppendUnique
	// behaviourAppendStrictUnique: a sequence Compose APPENDS — and then
	// refuses the whole project if the appended result repeats an element.
	//
	// Observed, not assumed. `docker compose config` over two files that each
	// declare `security_opt: [no-new-privileges:true]` exits with
	//
	//	services.a.security_opt items at 0 and 1 are equal
	//
	// while two files declaring DIFFERENT entries merge to both of them. So
	// Compose does not deduplicate these: it concatenates and validates.
	//
	// The merge therefore concatenates too — deduplicating would silently
	// produce a model for a document Compose will not load, which is this
	// engine's characteristic failure (a confident wrong answer, not a crash).
	// The repeat is reported as a finding instead, the same way a list meeting
	// a mapping is: the file is legal YAML and the resolved model is honest
	// about what the files say, and the reader is told Compose will reject it.
	behaviourAppendStrictUnique
)

func (b behaviour) String() string {
	switch b {
	case behaviourReplace:
		return "replace"
	case behaviourAppendUnique:
		return "appendUnique"
	case behaviourAppendStrictUnique:
		return "appendStrictUnique"
	}
	return "default"
}

// uniqueKeyFn extracts the identity of one sequence element. It reports false
// when the element has no identity it can read — an element that cannot be
// keyed is appended rather than guessed at.
type uniqueKeyFn func(v *Value) (string, bool)

// rule is one row. Pattern segments are matched literally except "*", which
// matches exactly one segment — `services.*.ports` is every service's ports.
type rule struct {
	pattern  string
	how      behaviour
	key      uniqueKeyFn
	keyName  string // for explain/debug output
	specNote string
}

// mergeTable is the table. Ordered for reading; lookup is by exact pattern, so
// order carries no meaning and a row can be inserted anywhere sensible.
//
// compose-spec §13 in three sentences: mappings merge key-wise; sequences
// append; except that a handful of keys REPLACE because appending would be
// nonsense, and a larger handful of keys carry a unique resource identifier and
// merge element-wise under it.
// PROVENANCE OF EVERY ROW BELOW IS STATED, AND ONLY §13 MAY SAY "§13".
//
// An earlier version of this table carried "§13:" notes on rows §13 says
// nothing about — devices, env_file, environment, and twenty more. That defeats
// the one promise AD-4 makes about this file, which is that it can be diffed
// against the spec by inspection: a fabricated citation reads as verified.
//
// §13 names SEVEN attributes and no others:
//
//	Replaced:        command, entrypoint, healthcheck.test
//	Unique resource: ports    → {ip, target, published, protocol}
//	                 volumes  → target
//	                 secrets  → target
//	                 configs  → target
//
// Everything else in §13 is "mappings merge key-wise, sequences append".
//
// The rows below §13's are therefore labelled for what they are, in two
// groups, neither of which may say "§13":
//
//   - Compose NORMALISES several list-shaped attributes into mappings before
//     merging, so merging them on the element's name is that mapping's
//     key-wise merge arriving one layer early.
//   - Compose DEDUPLICATES a further set, and CONCATENATES-THEN-REJECTS three
//     more. Neither is derivable from the spec text; both are measured. The
//     authority is the differential oracle in cmd/differential, which runs
//     `docker compose config` over the multi-file fixtures in
//     testdata/differential and compares its output against this merge. Those
//     rows name the oracle and the fixture, so the claim is checkable by
//     re-running it rather than by trusting the comment.
//
// A row justified only by "it seems tidy" is still not allowed here. Where the
// oracle says Compose appends, the row is absent and the §13 default applies.
var mergeTable = []rule{
	// ---- §13 "Replaced" -------------------------------------------------
	//
	// These three are the ones every naive deep merge gets wrong, and getting
	// them wrong produces a container that runs a concatenation of two
	// different command lines.
	{pattern: "services.*.command", how: behaviourReplace, specNote: "§13: command is replaced, never appended"},
	{pattern: "services.*.entrypoint", how: behaviourReplace, specNote: "§13: entrypoint is replaced, never appended"},
	{pattern: "services.*.healthcheck.test", how: behaviourReplace, specNote: "§13: healthcheck.test is replaced, never appended"},

	// ---- §13 "Unique resource" ------------------------------------------
	//
	// Four attributes, and the spec states the identity for each. Note that
	// secrets and configs are keyed on TARGET, not source: two secrets from one
	// source mounted at two paths are two mounts, and keying them on source
	// deletes one of them silently.
	{pattern: "services.*.ports", how: behaviourAppendUnique, key: keyPort, keyName: "host_ip:published:target/protocol",
		specNote: "§13: ports are unique on {ip, target, published, protocol}"},
	{pattern: "services.*.volumes", how: behaviourAppendUnique, key: keyMountTarget, keyName: "target",
		specNote: "§13: volumes are unique on target"},
	{pattern: "services.*.secrets", how: behaviourAppendUnique, key: keySecretTarget, keyName: "target",
		specNote: "§13: secrets are unique on target"},
	{pattern: "services.*.configs", how: behaviourAppendUnique, key: keyConfigTarget, keyName: "target",
		specNote: "§13: configs are unique on target"},

	// ---- Compose behaviour, NOT §13 -------------------------------------
	//
	// Each of these is a mapping that Compose also accepts as a list. Compose
	// normalises the list into the mapping before it merges anything, so the
	// two files' entries meet as mapping keys and the later one wins by name.
	// Appending instead would leave a container carrying `FOO=1` and `FOO=2`,
	// which is not a shape Compose can produce.
	//
	// The note on every one of these says so, rather than citing a clause that
	// does not exist.
	{pattern: "services.*.environment", how: behaviourAppendUnique, key: keyBeforeEquals, keyName: "name", specNote: normalisedToMapping},
	{pattern: "services.*.labels", how: behaviourAppendUnique, key: keyBeforeEquals, keyName: "name", specNote: normalisedToMapping},
	{pattern: "services.*.annotations", how: behaviourAppendUnique, key: keyBeforeEquals, keyName: "name", specNote: normalisedToMapping},
	{pattern: "services.*.build.args", how: behaviourAppendUnique, key: keyBeforeEquals, keyName: "name", specNote: normalisedToMapping},
	{pattern: "services.*.build.labels", how: behaviourAppendUnique, key: keyBeforeEquals, keyName: "name", specNote: normalisedToMapping},
	{pattern: "services.*.sysctls", how: behaviourAppendUnique, key: keyBeforeEquals, keyName: "name", specNote: normalisedToMapping},
	{pattern: "services.*.extra_hosts", how: behaviourAppendUnique, key: keyBeforeColonOrEquals, keyName: "host", specNote: normalisedToMapping},
	{pattern: "services.*.networks", how: behaviourAppendUnique, key: keySelf, keyName: "network", specNote: normalisedToMapping},
	{pattern: "services.*.depends_on", how: behaviourAppendUnique, key: keySelf, keyName: "service", specNote: normalisedToMapping},

	// ---- Compose behaviour, NOT §13: measured against the CLI ------------
	//
	// These deduplicate. §13 does not say so and neither does any other
	// clause — the evidence is the oracle: `cmd/differential` runs
	// `docker compose config` over two-file projects and compares its output
	// against the resolved model, and on testdata/differential/dedupe-rows an
	// entry declared in BOTH files comes back from Compose exactly once.
	//
	// These rows were deleted once on the argument that §13 licenses no
	// dedupe. That argument was right about the citations and wrong about the
	// behaviour, and the deletion made every one of these diverge from
	// Compose. They are restored with the provenance they actually have, which
	// a future reader can re-check in one command:
	//
	//	make differential          # or: cd testdata/differential/dedupe-rows
	//	                           #      && docker compose config
	//
	// Dedupe preserves the FIRST occurrence's position and appends whatever is
	// new in the later file, which is what mergeSequenceUnique does: the
	// oracle on base `[A_ONE, B_TWO]` + override `[C_THREE, A_ONE]` returns
	// `A_ONE, B_TWO, C_THREE`.
	{pattern: "services.*.cap_add", how: behaviourAppendUnique, key: keySelf, keyName: "capability", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.cap_drop", how: behaviourAppendUnique, key: keySelf, keyName: "capability", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.dns", how: behaviourAppendUnique, key: keySelf, keyName: "address", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.dns_opt", how: behaviourAppendUnique, key: keySelf, keyName: "option", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.dns_search", how: behaviourAppendUnique, key: keySelf, keyName: "domain", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.expose", how: behaviourAppendUnique, key: keySelf, keyName: "port", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.links", how: behaviourAppendUnique, key: keySelf, keyName: "link", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.profiles", how: behaviourAppendUnique, key: keySelf, keyName: "profile", specNote: dedupeVerifiedByCLI},
	{pattern: "services.*.build.tags", how: behaviourAppendUnique, key: keySelf, keyName: "tag", specNote: dedupeVerifiedByCLI},

	// tmpfs is keyed on the WHOLE entry, not on the mount path, and that is
	// deliberate. Compose collapses `- /tmp` written twice, but on `/tmp:size=64m`
	// against `/tmp` it does NOT pick one — it fails with
	//
	//	services.a.tmpfs[1]: target /tmp already mounted as services.a.tmpfs[0]
	//
	// Keying on the path would silently choose a winner where Compose refuses,
	// which is exactly the trade this table must not make.
	{pattern: "services.*.tmpfs", how: behaviourAppendUnique, key: keySelf, keyName: "entry", specNote: dedupeVerifiedByCLI +
		"; keyed on the whole entry because `/tmp:size=64m` against `/tmp` is a Compose ERROR, not a merge"},

	// devices is the one in this group that is NOT whole-value equality.
	// `docker compose config` on `/dev/ttyS0:/dev/x` + `/dev/sda:/dev/x`
	// returns ONE device, `{source: /dev/sda, target: /dev/x}` — the later
	// declaration wins the container path — while `/dev/x:/dev/a` +
	// `/dev/x:/dev/b` returns two. The identity is the target, like a volume.
	{pattern: "services.*.devices", how: behaviourAppendUnique, key: keyMountTarget, keyName: "target",
		specNote: "Verified against `docker compose config`, not §13: two devices sharing a container path collapse to the later one; the identity is the target, not the whole entry"},

	// ---- Compose APPENDS these, then REJECTS the repeat ------------------
	//
	// See behaviourAppendStrictUnique. Deduplicating here would hide a broken
	// project: `docker compose config` refuses the document outright.
	{pattern: "services.*.security_opt", how: behaviourAppendStrictUnique, key: keySelf, keyName: "option", specNote: strictVerifiedByCLI},
	{pattern: "services.*.group_add", how: behaviourAppendStrictUnique, key: keySelf, keyName: "group", specNote: strictVerifiedByCLI},
	{pattern: "services.*.volumes_from", how: behaviourAppendStrictUnique, key: keySelf, keyName: "service", specNote: strictVerifiedByCLI},

	// ---- Rows that stay deleted -----------------------------------------
	//
	// build.platforms, build.cache_from, deploy.placement.constraints and
	// deploy.placement.preferences used to dedupe here with a fabricated "§13:"
	// citation. The oracle says Compose APPENDS all four — the repeat comes
	// back twice — so the deletion was correct and they take the §13 default.
	//
	// env_file also stays unlisted: Compose folds it into `environment` before
	// anything observable happens, so there is no oracle evidence either way
	// and a rule invented in the absence of evidence is what this table exists
	// to prevent.
}

// normalisedToMapping is the honest provenance note for the rows above: they
// encode what Compose does, and Compose's reason is normalisation, not §13.
const normalisedToMapping = "Compose behaviour, not §13: Compose normalises this list form into a mapping before merging, so entries meet by name"

// dedupeVerifiedByCLI labels a row whose only authority is the Compose CLI's
// observed behaviour. It names the oracle and the fixture rather than a clause,
// so a reader who doubts it can re-run the thing that established it instead of
// looking up a section that does not say this.
const dedupeVerifiedByCLI = "Verified against `docker compose config` (cmd/differential, testdata/differential/dedupe-rows), NOT §13: " +
	"an entry declared in two files comes back from Compose once"

// strictVerifiedByCLI labels the three attributes Compose concatenates and then
// validates for uniqueness — see testdata/differential/compose-rejects-duplicate.
const strictVerifiedByCLI = "Verified against `docker compose config` (cmd/differential, testdata/differential/compose-rejects-duplicate), NOT §13: " +
	"Compose appends these and then refuses the project, \"items at 0 and 1 are equal\"; the repeat is kept and reported, never silently collapsed"

// ruleIndex is the table keyed for lookup, built once.
var ruleIndex = func() map[string]rule {
	m := make(map[string]rule, len(mergeTable))
	for _, r := range mergeTable {
		m[r.pattern] = r
	}
	return m
}()

// ruleFor returns the row governing a config path.
//
// The path is generalised before lookup: every segment that addresses a
// specific instance — a service name, a sequence index — becomes `*`, so one
// row covers every service. Top-level section names are kept literal, because
// `services` and `networks` are different sections and must not collapse into
// each other.
func ruleFor(p Path) rule {
	if r, ok := ruleIndex[patternOf(p)]; ok {
		return r
	}
	return rule{pattern: patternOf(p), how: behaviourDefault}
}

// patternOf generalises a path into a table pattern. The second segment of a
// top-level section is an instance name and becomes `*`; so does any all-digit
// segment, which addresses a sequence element.
func patternOf(p Path) string {
	if len(p) == 0 {
		return ""
	}
	segs := make([]string, len(p))
	for i, s := range p {
		switch {
		case i == 1 && isSectionKey(p[0]):
			segs[i] = "*"
		case isIndexSegment(s):
			segs[i] = "*"
		default:
			segs[i] = s
		}
	}
	return strings.Join(segs, ".")
}

// isSectionKey reports whether a top-level key holds named instances.
func isSectionKey(s string) bool {
	switch s {
	case "services", "networks", "volumes", "configs", "secrets", "models":
		return true
	}
	return false
}

// ---- key extractors --------------------------------------------------------
//
// Every one of these reads an element as the file wrote it. They never coerce,
// and they report false rather than inventing an identity for a shape they do
// not recognise — an element with no identity is appended, which is the safe
// answer: it duplicates at worst, where a wrong identity silently deletes.

func keySelf(v *Value) (string, bool) {
	if v == nil || v.kind != KindScalar {
		return "", false
	}
	return strings.TrimSpace(v.scalar), true
}

// keyBeforeEquals reads `NAME=value` and `NAME`. It is the identity of an
// environment variable, a label and a build arg alike.
func keyBeforeEquals(v *Value) (string, bool) {
	s, ok := keySelf(v)
	if !ok {
		return "", false
	}
	if k, _, found := strings.Cut(s, "="); found {
		return k, true
	}
	return s, true
}

// keyBeforeColonOrEquals reads `host:ip` and `host=ip`, the two spellings
// extra_hosts accepts.
func keyBeforeColonOrEquals(v *Value) (string, bool) {
	s, ok := keySelf(v)
	if !ok {
		return "", false
	}
	if i := strings.IndexAny(s, ":="); i >= 0 {
		return s[:i], true
	}
	return s, true
}

// keySecretTarget and keyConfigTarget read the MOUNT PATH of a secret or a
// config — §13's unique key for both.
//
// Keying on `source` (which is what this file used to do) is wrong in both
// directions and both are silent:
//
//	secrets:                      secrets:
//	  - source: cert                - source: cert
//	    target: /etc/a                target: /etc/b
//
// is two mounts, and merging them on `source` deletes one. Conversely two
// different sources mounted at the same target are one mount, and appending
// them leaves a container told to write two files to one path.
//
// The short form — a bare name — carries no target, so the DEFAULT target is
// computed, because that is the path the container actually gets: Compose
// mounts a secret at /run/secrets/<source> and a config at /<source>. Comparing
// a short form against a long form any other way would make `- cert` and
// `{source: cert, target: /run/secrets/cert}` look like two different mounts
// when they are the same one.
func keySecretTarget(v *Value) (string, bool) { return keyMountedResource(v, "/run/secrets/") }

func keyConfigTarget(v *Value) (string, bool) { return keyMountedResource(v, "/") }

func keyMountedResource(v *Value, defaultDir string) (string, bool) {
	if s, ok := keySelf(v); ok {
		if s = strings.TrimSpace(s); s == "" {
			return "", false
		}
		return defaultDir + s, true
	}
	if v != nil && v.kind == KindMapping {
		if t, ok := v.mapping.Get("target"); ok && t.kind == KindScalar {
			if s := strings.TrimSpace(t.scalar); s != "" {
				// A target may be written relative — Compose resolves it under
				// the same default directory — so it is normalised the same way
				// the short form is, or `target: cert` and `- cert` would not
				// meet.
				if !strings.HasPrefix(s, "/") {
					return defaultDir + s, true
				}
				return s, true
			}
		}
		// No target: the mount lands at the default path for its source.
		if src, ok := v.mapping.Get("source"); ok && src.kind == KindScalar {
			if s := strings.TrimSpace(src.scalar); s != "" {
				return defaultDir + s, true
			}
		}
	}
	return "", false
}

// keyMountTarget reads the in-container path of a volume or device.
//
// Short syntax is `[source:]target[:mode]`, and the source may be a Windows
// path with a drive letter — `C:\data:/data` — so the split counts from the
// right rather than the left, and a lone segment IS the target (an anonymous
// volume).
func keyMountTarget(v *Value) (string, bool) {
	if v != nil && v.kind == KindMapping {
		if t, ok := v.mapping.Get("target"); ok && t.kind == KindScalar {
			return t.scalar, true
		}
		return "", false
	}
	s, ok := keySelf(v)
	if !ok || s == "" {
		return "", false
	}
	parts := splitMount(s)
	switch len(parts) {
	case 1:
		return parts[0], true
	default:
		return parts[1], true
	}
}

// splitMount splits a short-syntax mount into at most source, target, mode,
// tolerating a Windows drive letter in the source.
func splitMount(s string) []string {
	parts := strings.Split(s, ":")
	// `C:\data:/data` splits into 3 with the first two belonging together.
	if len(parts) > 1 && len(parts[0]) == 1 && isDriveLetter(parts[0][0]) {
		parts = append([]string{parts[0] + ":" + parts[1]}, parts[2:]...)
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return parts
}

func isDriveLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// keyPort reads a published-port declaration in either syntax.
//
// Short syntax is `[[host_ip:]host_port:]container_port[/protocol]`, where
// host_ip may itself contain colons if it is IPv6 — so the protocol is split
// off first from the right, then at most two colon-separated fields are taken
// from the right and whatever remains is the address.
//
// The identity is the whole tuple rather than the container port alone. Two
// declarations that publish the same container port on different host ports are
// two different publications, and collapsing them would silently delete one.
func keyPort(v *Value) (string, bool) {
	if v != nil && v.kind == KindMapping {
		get := func(k string) string {
			if e, ok := v.mapping.Get(k); ok && e.kind == KindScalar {
				return e.scalar
			}
			return ""
		}
		target := get("target")
		if target == "" {
			return "", false
		}
		proto := get("protocol")
		if proto == "" {
			proto = "tcp"
		}
		return get("host_ip") + "|" + get("published") + "|" + target + "|" + proto, true
	}
	s, ok := keySelf(v)
	if !ok || s == "" {
		return "", false
	}
	proto := "tcp"
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		proto, s = s[i+1:], s[:i]
	}
	var hostIP, published, target string
	if i := strings.LastIndexByte(s, ':'); i < 0 {
		target = s
	} else {
		target = s[i+1:]
		rest := s[:i]
		if j := strings.LastIndexByte(rest, ':'); j < 0 {
			published = rest
		} else {
			published, hostIP = rest[j+1:], rest[:j]
		}
	}
	return hostIP + "|" + published + "|" + target + "|" + proto, true
}
