package edit

// Adding something that is not there yet — stories 7.3 and 7.4.
//
// This file is a PLANNER, not an engine. It performs no splice, computes no
// offset and writes no byte: it turns "the reader named a service" into the
// operations `run` already applies as one atomic edit, and it refuses — with a
// typed sentinel, before a single operation is staged — when the answer would
// be a damaged file or a guess about what the reader meant.
//
// Why a planner in Go rather than three lines in the panel: the same request
// has to come from `composure add`, from the JSON-RPC server and from the
// extension, and "where does a service go, and when is it refused" cannot have
// three answers. CLI before UI, and one implementation behind both doors.
//
// Two refusals are new here and neither is a formatting opinion:
//
//	a duplicate name    InsertKey would happily write a second `cache:`, and
//	                    YAML's last-one-wins would then discard one of the two
//	                    services with nothing on screen to say so.
//
//	a value that would  R4.1 preserves the quoting style a file already has. A
//	not survive bare    new value has none, so the tool imposes none — and a
//	                    value YAML would read back as something else (`3.10`
//	                    is the float 3.1, `yes` is a boolean to half the
//	                    parsers in the world) is a question only the reader can
//	                    settle. Quoting it for them is exactly the formatting
//	                    opinion R7.4 refuses in the other grammar.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// AddKinds is every kind of thing that can be declared, in the order a reader
// meets them. A kind's top-level block is its name plus `s` — `service` goes
// under `services`, `secret` under `secrets` — which is why there is no
// per-resource branch anywhere below. internal/schema already treats the four
// resource kinds uniformly and a switch here would be the fourth place they
// are enumerated.
var AddKinds = []string{"service", "network", "volume", "config", "secret"}

// Sentinels. Every one means NOTHING was written and nothing was staged.
var (
	// ErrUnknownKind is a kind outside AddKinds. A caller error rather than a
	// refusal: no reader can hit it, only a client that made the name up.
	ErrUnknownKind = errors.New("unknown kind")
	// ErrDuplicateName is a name the configuration already declares.
	ErrDuplicateName = errors.New("that name is already declared")
	// ErrNoName is an empty name. There is nothing to add.
	ErrNoName = errors.New("nothing was named")
	// ErrNoImage is a service with no image. A `cache:` with nothing under it
	// is not a valid stack, and leaving one on disk is the partial write the
	// whole write path exists to make impossible.
	ErrNoImage = errors.New("a service needs an image")
	// ErrNeedsQuoting is a value or a name that would not survive as a bare
	// scalar: YAML would read back something other than what was typed.
	ErrNeedsQuoting = errors.New("that would not survive unquoted")
	// ErrNameTooLong is a name YAML cannot carry as a key at all.
	//
	// It is NOT a quoting problem and it is a separate sentinel for exactly that
	// reason: every other refusal here ends with "quote it yourself and it goes
	// in as typed", and that advice is false here. YAML caps an implicit key —
	// quoted or not — at 1024 characters, so the only workaround is a shorter
	// name. A refusal that offers a workaround which does not work is worse than
	// no refusal, because the reader tries it.
	ErrNameTooLong = errors.New("that name is too long to be written as a key")
)

// implicitKeyLimit is YAML's cap on an implicit mapping key, in characters
// (runes, measured: 1024 runes of `é` is accepted, 1025 of `n` is not, and the
// cap is on the key rather than the line — the same key is refused at indent 0,
// 2 and 4).
//
// It is stated here as a number only so the refusal can SAY it. The check that
// decides is the readback below, which would catch a change of limit in the
// parser without anybody editing this constant.
const implicitKeyLimit = 1024

// Add is the reader's request: a kind, a name, and — for a service — the image.
type Add struct {
	// File is the file the declaration is written into. Staging is per-file
	// (R4.7): the reader is told which file before the write.
	File string
	// Kind is one of AddKinds.
	Kind string
	// Name is the service or resource name, exactly as the reader typed it.
	Name string
	// Value is a service's image. A resource takes none: a network with a
	// value is `frontend: bridge`, which is not a declaration.
	Value string
	// Merged is the resolved project, when the caller has one. It is how a
	// name declared in ANOTHER file of the chain is caught, which is where
	// last-one-wins actually happens. Optional: an empty stack does not
	// resolve at all, and it is exactly the stack a reader most needs to add a
	// service to.
	Merged *resolve.Project
}

// Block is the top-level mapping a kind lives under.
func (a Add) Block() string { return a.Kind + "s" }

// Plan turns the request into the operations that perform it, or refuses.
//
// The operations are the ordinary closed set — `insert_key`, and nothing this
// package invented — so `run` applies them exactly as it applies any other
// request: staleness checked before the first splice, each one re-located
// against the buffer the last produced, one validate, one diff, one write. The
// service and its image are therefore one edit and one undo, never a name
// written now and an image written afterwards.
func Plan(a Add) ([]Op, error) {
	if !known(a.Kind) {
		return nil, fmt.Errorf("%w %q: it is one of %s", ErrUnknownKind, a.Kind, strings.Join(AddKinds, ", "))
	}
	if strings.TrimSpace(a.Name) == "" {
		return nil, fmt.Errorf("%w: give the %s a name", ErrNoName, a.Kind)
	}
	if err := bareKey(a.Name, "name"); err != nil {
		return nil, err
	}

	service := a.Kind == "service"
	if service && strings.TrimSpace(a.Value) == "" {
		return nil, fmt.Errorf("%w: %q would be written with nothing under it, which is not a stack anything can run",
			ErrNoImage, a.Name)
	}
	if !service && a.Value != "" {
		return nil, fmt.Errorf("a %s takes no value: %q would be written as `%s: %s`, which declares nothing",
			a.Kind, a.Value, a.Name, a.Value)
	}
	if service {
		if err := bare(a.Value, "image"); err != nil {
			return nil, err
		}
	}

	src, err := readSource(a.File)
	if err != nil {
		return nil, err
	}
	block := a.Block()
	if err := notDeclared(src, a.File, block, a.Name, a.Merged); err != nil {
		return nil, err
	}

	var ops []Op
	// The block itself, when the file has none. Story 7.2's root insert is
	// what makes this reachable — before it, `networks:` on a file without one
	// was a bare error the reader was shown as a fault.
	if _, _, err := strategy.Locate(src, []string{block}); err != nil {
		ops = append(ops, Op{Operation: OpInsertKey, At: "", Key: block})
	}
	ops = append(ops, Op{Operation: OpInsertKey, At: block, Key: a.Name})
	if service {
		// The path is RENDERED from its segments, never joined with a `.`.
		//
		// `foo.bar` and `v1.2` are legal compose service names — the vendored
		// schema's own pattern is ^[a-zA-Z0-9._-]+$ — and `block + "." + name`
		// hands resolve.ParsePath a string it splits back into the wrong
		// segments, so `services.foo.bar` looked for a service `foo` with a key
		// `bar`. The reader saw `path segment "foo" not found`: a fault, for a
		// request that was perfectly well formed.
		//
		// resolve.Path.String already quotes a segment containing `.`, `[`, `]`
		// or `"`, and ParsePath reads that quoting back — so the round trip is
		// the path type's own, not a rule invented here.
		ops = append(ops, Op{
			Operation: OpInsertKey,
			At:        resolve.Path{block, a.Name}.String(),
			Key:       "image",
			Value:     a.Value,
		})
	}
	return ops, nil
}

func known(kind string) bool {
	for _, k := range AddKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// notDeclared refuses a name the configuration already carries, naming the file
// and line that declares it.
//
// Two lookups, because they answer two different questions. The file's own
// bytes are what the insert would write a duplicate KEY into, and the merged
// project is where a name declared in an override silently wins. The merged
// check is skipped when the caller has no project — an empty stack does not
// resolve, and refusing to add a service to one because of that would make the
// state EXPERIENCE.md calls a starting point into a dead end.
func notDeclared(src []byte, file, block, name string, merged *resolve.Project) error {
	if idx, _, err := strategy.Locate(src, []string{block, name}); err == nil {
		return fmt.Errorf("%w: %s.%s is already declared at %s:%d", ErrDuplicateName, block, name, file, idx+1)
	}
	if merged == nil {
		return nil
	}
	top, ok := merged.Root().At(resolve.Path{block})
	if !ok || top.Kind() != resolve.KindMapping {
		return nil
	}
	origin, ok := top.Map().KeyOrigin(name)
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: %s.%s is already declared at %s:%d", ErrDuplicateName, block, name, origin.File, origin.Line)
}

func readSource(file string) ([]byte, error) {
	if strings.TrimSpace(file) == "" {
		return nil, fmt.Errorf("edit: no file named")
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	return src, nil
}

// bare refuses text that would not survive being written as a plain scalar.
//
// The test is not a character blocklist with a guess attached: the text is
// parsed as the value it will become, and accepted only when YAML reads back
// exactly the bytes the reader typed, as a string. That catches the cases the
// story names — `3.10` (a float that loses a digit), `yes` (a boolean in YAML
// 1.1 and to compose-go's ancestors), a `#` that starts a comment, a trailing
// space that vanishes, a leading `-` that starts a sequence — and it catches
// the ones nobody thought of, which is the point of testing the property
// rather than the list.
//
// Text the reader has quoted THEMSELVES is accepted and written through
// verbatim. That is the whole workaround the refusal offers, so it has to work.
func bare(text, what string) error {
	if strings.ContainsAny(text, "\n\r") {
		return fmt.Errorf("%w: the %s contains a line break, and deciding where your lines break is not this tool's call. Write it on one line, or quote it yourself", ErrNeedsQuoting, what)
	}
	if strings.Contains(text, "\t") {
		return fmt.Errorf("%w: the %s contains a tab, which YAML does not allow in indentation and reads unpredictably here. Quote it yourself and it goes in exactly as typed", ErrNeedsQuoting, what)
	}
	if strings.HasPrefix(text, "-") {
		return fmt.Errorf("%w: the %s starts with `-`, which begins a list entry in YAML. Quote it yourself — \"%s\" — and Composure writes it as you typed it", ErrNeedsQuoting, what, text)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("x: "+text+"\n"), &doc); err != nil {
		return fmt.Errorf("%w: as a %s, `%s` is not a value YAML can read on its own (%v). Quote it yourself and Composure writes it exactly as typed",
			ErrNeedsQuoting, what, text, err)
	}
	if len(doc.Content) != 1 || len(doc.Content[0].Content) != 2 {
		return fmt.Errorf("%w: `%s` is more than one value, so it cannot be written as a %s. Quote it yourself and it goes in as one",
			ErrNeedsQuoting, text, what)
	}
	node := doc.Content[0].Content[1]
	if node.Kind == yaml.ScalarNode &&
		(node.Style&yaml.DoubleQuotedStyle != 0 || node.Style&yaml.SingleQuotedStyle != 0) {
		// The reader settled it. Their bytes, unchanged.
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: `%s` is a collection, not a %s. Write the %s as one value and add the rest of it afterwards",
			ErrNeedsQuoting, text, what, what)
	}
	if node.Style != 0 {
		return fmt.Errorf("%w: `%s` is a block scalar, which spans lines. Quote it yourself and Composure writes it as one",
			ErrNeedsQuoting, text)
	}
	if node.Tag != "!!str" {
		return fmt.Errorf("%w: YAML reads `%s` as %s, not as text — and whether you meant the text or the %s is a question only you can answer. Quote it yourself — \"%s\" — and it goes in as typed",
			ErrNeedsQuoting, text, tagName(node.Tag), tagName(node.Tag), text)
	}
	if node.Value != text {
		return fmt.Errorf("%w: written bare, `%s` would be read back as `%s` — %s. Quote it yourself and Composure writes every byte of it",
			ErrNeedsQuoting, text, node.Value, whyItChanged(text))
	}
	if boolish(text) {
		return fmt.Errorf("%w: `%s` is a boolean to a YAML 1.1 reader and text to a 1.2 one, and container tooling is split between them. Quote it yourself — \"%s\" — and there is nothing left to interpret",
			ErrNeedsQuoting, text, text)
	}
	return nil
}

// bareKey is bare for text that becomes a mapping KEY rather than a value.
//
// The distinction is not pedantry, it is a defect this repository shipped. bare
// tests the text as a VALUE — `yaml.Unmarshal("x: " + text)` — and a value has
// no length limit, so a 1030-character service name passed every check, was
// written, and produced a file the resolver could not read: `could not find
// expected ':'`, exit 0, "2 lines added". The engine's own validate did not
// catch it either, because goccy accepts an over-long implicit key that yaml.v3
// refuses, and validate parsed with goccy alone.
//
// So the name is tested as what it will BE. Same shape as bare: the text is
// parsed in the position it will occupy and accepted only when YAML reads the
// key back as the bytes the reader typed.
func bareKey(text, what string) error {
	if err := bare(text, what); err != nil {
		return err
	}
	if n := len([]rune(text)); n > implicitKeyLimit {
		return fmt.Errorf("%w: the %s is %d characters and YAML stops reading a key at %d, so the file this would write cannot be parsed back. Quoting does not lift the limit — shorten the %s",
			ErrNameTooLong, what, n, implicitKeyLimit, what)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text+": x\n"), &doc); err != nil {
		return fmt.Errorf("%w: as a %s, `%s` is not something YAML can read back as a key (%v)",
			ErrNameTooLong, what, truncate(text), err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) != 2 {
		return fmt.Errorf("%w: `%s` does not read back as one key", ErrNeedsQuoting, truncate(text))
	}
	key := doc.Content[0].Content[0]
	if key.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: `%s` reads back as a collection, not a %s", ErrNeedsQuoting, truncate(text), what)
	}
	if key.Style&yaml.DoubleQuotedStyle != 0 || key.Style&yaml.SingleQuotedStyle != 0 {
		// The reader quoted it themselves; bare has already accepted their
		// bytes and the key parses. Their quoting, unchanged.
		return nil
	}
	if key.Value != text {
		return fmt.Errorf("%w: written as a key, `%s` would be read back as `%s`",
			ErrNeedsQuoting, truncate(text), truncate(key.Value))
	}
	return nil
}

// truncate keeps a refusal readable when the thing being refused is a thousand
// characters long. The whole point of this refusal is that the name is enormous;
// printing all of it buries the sentence that explains why.
func truncate(text string) string {
	r := []rune(text)
	if len(r) <= 60 {
		return text
	}
	return string(r[:57]) + "..."
}

// boolish is the YAML 1.1 boolean vocabulary. yaml.v3 reads these as strings,
// which is exactly why they are named: a file this tool wrote would mean one
// thing here and another in a tool that follows 1.1, and the reader would never
// see the disagreement.
func boolish(text string) bool {
	switch strings.ToLower(text) {
	case "y", "n", "yes", "no", "on", "off", "true", "false":
		return true
	}
	return false
}

func tagName(tag string) string {
	switch tag {
	case "!!bool":
		return "a boolean"
	case "!!int":
		return "a whole number"
	case "!!float":
		return "a number"
	case "!!null":
		return "nothing at all"
	case "!!timestamp":
		return "a date"
	}
	return "a " + strings.TrimPrefix(tag, "!!")
}

func whyItChanged(text string) string {
	switch {
	case strings.Contains(text, "#"):
		return "everything from the `#` is a comment"
	case strings.TrimSpace(text) != text:
		return "the space at the edge is not part of a plain value"
	}
	return "a plain scalar is not read back character for character"
}
