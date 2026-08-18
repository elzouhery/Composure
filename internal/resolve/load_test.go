package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- story 1.6: explicit -f chains -----------------------------------------

func TestChainMergesLeftToRightWithNoSpecialCasing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.yaml", "services:\n  web:\n    image: base\n    environment:\n      A: base\n")
	write(t, dir, "staging.yaml", "services:\n  web:\n    image: staging\n    environment:\n      B: staging\n")
	write(t, dir, "local.yaml", "services:\n  web:\n    image: local\n")
	write(t, dir, "fourth.yaml", "services:\n  web:\n    environment:\n      C: fourth\n")

	p, err := Files(
		filepath.Join(dir, "base.yaml"),
		filepath.Join(dir, "staging.yaml"),
		filepath.Join(dir, "local.yaml"),
		filepath.Join(dir, "fourth.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	img, _ := p.At(ParsePath("services.web.image"))
	if img.Scalar() != "local" {
		t.Errorf("image = %q, want the last file that set it", img.Scalar())
	}
	// Origin.Step identifies the position in the chain, and the full history is
	// available in order, oldest first.
	if img.Origin().Step != 2 {
		t.Errorf("Step = %d, want 2", img.Origin().Step)
	}
	hist := img.Overrides()
	if len(hist) != 2 || hist[0].Value != "base" || hist[1].Value != "staging" {
		t.Fatalf("override history = %+v, want base then staging", hist)
	}
	if hist[0].Origin.Step != 0 || hist[1].Origin.Step != 1 {
		t.Errorf("history steps = %d,%d, want 0,1", hist[0].Origin.Step, hist[1].Origin.Step)
	}
	// Everything each file contributed is still there.
	for _, k := range []string{"A", "B", "C"} {
		if _, ok := p.At(ParsePath("services.web.environment." + k)); !ok {
			t.Errorf("environment.%s did not survive the chain", k)
		}
	}
	if len(p.Files()) != 4 {
		t.Errorf("file list = %v, want four entries", p.Files())
	}
}

// Passing -f DISABLES the automatic override pickup, matching Compose. A
// project that has an override file and is resolved by name must not silently
// get it.
func TestExplicitFilesDisableTheOverridePickup(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", "services:\n  web:\n    image: base\n")
	write(t, dir, "compose.override.yaml", "services:\n  web:\n    image: overridden\n")

	explicit, err := Files(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, explicit, "services.web.image"); got != "base" {
		t.Errorf("with -f the image is %q; the override file must not be picked up", got)
	}
	if len(explicit.Files()) != 1 {
		t.Errorf("file list = %v, want only the named file", explicit.Files())
	}

	auto, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, auto, "services.web.image"); got != "overridden" {
		t.Errorf("resolving the DIRECTORY gave %q; the override file must be picked up", got)
	}
}

// The override file's name follows the base file's, so a legacy project does
// not pick up an override written for the new spelling.
func TestOverridePickupFollowsTheBaseFileName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  web:\n    image: base\n")
	write(t, dir, "docker-compose.override.yml", "services:\n  web:\n    image: legacy-override\n")
	write(t, dir, "compose.override.yaml", "services:\n  web:\n    image: wrong-one\n")

	p, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, p, "services.web.image"); got != "legacy-override" {
		t.Errorf("image = %q, want the override matching the base file's name", got)
	}
}

// A missing file in the chain is a typed error naming it, and NOTHING resolves
// — not a partial merge of the files that were there.
func TestMissingFileInAChainIsATypedErrorAndResolvesNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "services:\n  web:\n    image: nginx\n")
	missing := filepath.Join(dir, "nope.yaml")

	p, err := Files(filepath.Join(dir, "a.yaml"), missing)
	if p != nil {
		t.Error("a model was returned alongside the error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var fe *FileError
	if !errors.As(err, &fe) || fe.File != missing {
		t.Errorf("the error does not name the missing path: %v", err)
	}
}

// A later file in a chain need not be a whole compose project: an override that
// only tunes `networks:` is legal, and refusing it would refuse a real project.
func TestALaterFileNeedNotDeclareServices(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", "services:\n  web:\n    image: nginx\n")
	write(t, dir, "compose.override.yaml", "networks:\n  frontend:\n    driver: bridge\n")
	p, err := Dir(dir)
	if err != nil {
		t.Fatalf("an override with no services: was refused: %v", err)
	}
	if _, ok := p.At(ParsePath("networks.frontend.driver")); !ok {
		t.Error("the override's networks did not reach the model")
	}
}

// ---- story 1.7: include ----------------------------------------------------

func TestIncludeMergesAndKeepsItsOwnProvenance(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "shared/common.yaml", "services:\n  db:\n    image: postgres:16\n")
	write(t, dir, "compose.yaml", `
include:
  - shared/common.yaml
services:
  web:
    image: nginx
`)
	p, err := File(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, ok := p.At(ParsePath("services.db.image"))
	if !ok {
		t.Fatal("the included service is not in the model")
	}
	if db.Scalar() != "postgres:16" {
		t.Errorf("db image = %q", db.Scalar())
	}
	// Origins point at the included file's real path, and it is in the ordered
	// file list that Step indexes.
	if filepath.Base(db.Origin().File) != "common.yaml" {
		t.Errorf("db image origin = %s, want the included file", db.Origin().File)
	}
	files := p.Files()
	if files[db.Origin().Step].Path != db.Origin().File {
		t.Errorf("Step %d does not index the included file in %v", db.Origin().Step, files)
	}
	// The directive itself is not configuration and does not reach the model.
	if _, ok := p.At(ParsePath("include")); ok {
		t.Error("the include: directive survived into the model")
	}
}

// Every path is relative to the FILE THAT DECLARED IT, never the working
// directory. This is R1.2's whole content.
func TestIncludePathsAreRelativeToTheDeclaringFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stacks/inner/leaf.yaml", "services:\n  leaf:\n    image: leaf\n")
	write(t, dir, "stacks/mid.yaml", "include:\n  - inner/leaf.yaml\nservices:\n  mid:\n    image: mid\n")
	write(t, dir, "compose.yaml", "include:\n  - stacks/mid.yaml\nservices:\n  top:\n    image: top\n")

	p, err := File(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"top", "mid", "leaf"} {
		if _, ok := p.At(ParsePath("services." + name)); !ok {
			t.Errorf("%s is missing; recursion or relative resolution failed", name)
		}
	}
}

// A local declaration wins over an included one. The other order would let an
// include silently change the file that used it.
func TestIncludingFileOverridesWhatItIncludes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "common.yaml", "services:\n  web:\n    image: from-include\n    restart: always\n")
	write(t, dir, "compose.yaml", "include:\n  - common.yaml\nservices:\n  web:\n    image: local\n")

	p, err := File(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := p.At(ParsePath("services.web.image"))
	if img.Scalar() != "local" {
		t.Errorf("image = %q, want the including file's value", img.Scalar())
	}
	if hist := img.Overrides(); len(hist) != 1 || hist[0].Value != "from-include" {
		t.Errorf("override history = %+v, want the included value", hist)
	}
	if got := scalarAt(t, p, "services.web.restart"); got != "always" {
		t.Errorf("restart = %q; a key only the include sets must survive", got)
	}
}

// The long form: a mapping with a path list and a project_directory.
func TestIncludeLongForm(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "frag/a.yaml", "services:\n  a:\n    image: a\n")
	write(t, dir, "frag/b.yaml", "services:\n  a:\n    image: b\n  extra:\n    image: extra\n")
	write(t, dir, "compose.yaml", `
include:
  - project_directory: frag
    path:
      - a.yaml
      - b.yaml
services:
  web:
    image: nginx
`)
	p, err := File(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The paths inside one entry merge as a chain, left to right.
	if got := scalarAt(t, p, "services.a.image"); got != "b" {
		t.Errorf("services.a.image = %q, want the later file's value", got)
	}
	if _, ok := p.At(ParsePath("services.extra")); !ok {
		t.Error("the second included file's service is missing")
	}
}

// A cycle is a typed error naming the cycle, not a stack overflow.
func TestIncludeCycleIsATypedError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "include:\n  - b.yaml\nservices:\n  a:\n    image: a\n")
	write(t, dir, "b.yaml", "include:\n  - a.yaml\nservices:\n  b:\n    image: b\n")

	p, err := File(filepath.Join(dir, "a.yaml"))
	if p != nil {
		t.Error("a model was returned for a cyclic include")
	}
	if !errors.Is(err, ErrIncludeCycle) {
		t.Fatalf("err = %v, want ErrIncludeCycle", err)
	}
	var ce *IncludeCycleError
	if !errors.As(err, &ce) || len(ce.Cycle) < 2 {
		t.Fatalf("the error does not carry the cycle: %v", err)
	}
	if !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
		t.Errorf("the message does not name both files: %v", err)
	}
}

// A file included twice by two different parents is a diamond, not a cycle.
func TestIncludeDiamondIsNotACycle(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "leaf.yaml", "services:\n  leaf:\n    image: leaf\n")
	write(t, dir, "left.yaml", "include:\n  - leaf.yaml\nservices:\n  left:\n    image: left\n")
	write(t, dir, "right.yaml", "include:\n  - leaf.yaml\nservices:\n  right:\n    image: right\n")
	write(t, dir, "compose.yaml", "include:\n  - left.yaml\n  - right.yaml\nservices:\n  top:\n    image: top\n")

	p, err := File(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatalf("a diamond include was refused as a cycle: %v", err)
	}
	if _, ok := p.At(ParsePath("services.leaf")); !ok {
		t.Error("the shared leaf is missing")
	}
}

// An include naming a file that is not there fails, naming it.
func TestIncludeOfAMissingFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", "include:\n  - nope.yaml\nservices:\n  web:\n    image: nginx\n")
	_, err := File(filepath.Join(dir, "compose.yaml"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// ---- story 1.8: extends ----------------------------------------------------

func TestExtendsInTheSameFile(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(`
services:
  base:
    image: alpine
    environment:
      A: base
      B: base
    restart: always
  child:
    extends: base
    environment:
      B: child
`), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, p, "services.child.image"); got != "alpine" {
		t.Errorf("child image = %q, want the base's", got)
	}
	if got := scalarAt(t, p, "services.child.environment.A"); got != "base" {
		t.Errorf("child A = %q", got)
	}
	if got := scalarAt(t, p, "services.child.environment.B"); got != "child" {
		t.Errorf("child B = %q, want the local value", got)
	}
	// The directive does not survive into the model: it has been applied.
	if _, ok := p.At(ParsePath("services.child.extends")); ok {
		t.Error("the extends: directive survived into the model")
	}
	// The base is untouched — extends copies, it does not move.
	if got := scalarAt(t, p, "services.base.environment.B"); got != "base" {
		t.Errorf("the base service was mutated: B = %q", got)
	}
}

// Provenance: an inherited value points at the BASE's definition site, and a
// locally overridden key reports the local site with the base value as what it
// overrode.
func TestExtendsProvenance(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(`
services:
  base:
    image: alpine
    environment:
      B: base-value
  child:
    extends: base
    environment:
      B: child-value
`), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := p.At(ParsePath("services.child.image"))
	if img.Origin().Line != 4 {
		t.Errorf("inherited image is anchored at line %d, want the base's line 4", img.Origin().Line)
	}
	b, _ := p.At(ParsePath("services.child.environment.B"))
	if b.Origin().Line != 10 {
		t.Errorf("the overriding value is anchored at line %d, want the local line 10", b.Origin().Line)
	}
	if hist := b.Overrides(); len(hist) != 1 || hist[0].Value != "base-value" {
		t.Fatalf("override history = %+v, want the base value", hist)
	} else if hist[0].Origin.Line != 6 {
		t.Errorf("the base value is anchored at line %d, want 6", hist[0].Origin.Line)
	}
}

// The extends rules DIFFER from the file-merge rules for exactly three keys.
// They name other services, and the services they name need not exist in the
// file being extended.
func TestExtendsDoesNotInheritDependsOnVolumesFromOrLinks(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(`
services:
  other:
    image: alpine
  base:
    image: alpine
    depends_on:
      - other
    volumes_from:
      - other
    links:
      - other
    cap_add:
      - NET_ADMIN
  child:
    extends: base
`), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"depends_on", "volumes_from", "links"} {
		if _, ok := p.At(ParsePath("services.child." + key)); ok {
			t.Errorf("child inherited %s; compose-spec excludes it from extends", key)
		}
	}
	// Everything else still comes across, through the same rule table.
	if _, ok := p.At(ParsePath("services.child.cap_add")); !ok {
		t.Error("child did not inherit cap_add")
	}
	// The base keeps them: they were excluded from the CONTRIBUTION, not
	// deleted from the project.
	if _, ok := p.At(ParsePath("services.base.depends_on")); !ok {
		t.Error("the base service lost its own depends_on")
	}
}

// Across files, with the path relative to the extending file.
func TestExtendsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "common/base.yaml", "services:\n  app:\n    image: alpine\n    environment:\n      A: from-base\n")
	write(t, dir, "stack/compose.yaml", `
services:
  web:
    extends:
      file: ../common/base.yaml
      service: app
    environment:
      B: local
`)
	p, err := File(filepath.Join(dir, "stack", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, p, "services.web.image"); got != "alpine" {
		t.Errorf("web image = %q", got)
	}
	if got := scalarAt(t, p, "services.web.environment.A"); got != "from-base" {
		t.Errorf("web A = %q", got)
	}
	v, _ := p.At(ParsePath("services.web.image"))
	if filepath.Base(v.Origin().File) != "base.yaml" {
		t.Errorf("inherited value's origin = %s, want the base file", v.Origin().File)
	}
	// The base file is in the ordered list, so Step means something.
	files := p.Files()
	if len(files) != 2 || files[v.Origin().Step].Path != v.Origin().File {
		t.Errorf("Step does not index the base file: files=%v origin=%v", files, v.Origin())
	}
	// The base service itself is NOT imported: extends copies keys, it does
	// not merge the other project.
	if _, ok := p.At(ParsePath("services.app")); ok {
		t.Error("extends imported the whole base project")
	}
}

// A chain of extends resolves base-first.
func TestExtendsChainsThroughSeveralServices(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(`
services:
  a:
    image: alpine
    environment:
      LEVEL: a
  b:
    extends: a
    environment:
      FROM_B: yes
  c:
    extends: b
`), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, p, "services.c.image"); got != "alpine" {
		t.Errorf("c image = %q; the chain did not resolve to the root", got)
	}
	if got := scalarAt(t, p, "services.c.environment.FROM_B"); got != "yes" {
		t.Errorf("c FROM_B = %q", got)
	}
}

func TestExtendsCycleIsATypedError(t *testing.T) {
	_, err := BytesWith("compose.yaml", []byte(`
services:
  a:
    extends: b
    image: alpine
  b:
    extends: a
    image: alpine
`), hermetic(nil))
	if !errors.Is(err, ErrExtendsCycle) {
		t.Fatalf("err = %v, want ErrExtendsCycle", err)
	}
	var ee *ExtendsError
	if !errors.As(err, &ee) || len(ee.Cycle) == 0 {
		t.Fatalf("the error does not carry the cycle: %v", err)
	}
}

func TestExtendsMissingTargetIsATypedError(t *testing.T) {
	_, err := BytesWith("compose.yaml", []byte(`
services:
  web:
    extends: nowhere
    image: nginx
`), hermetic(nil))
	if !errors.Is(err, ErrExtendsTarget) {
		t.Fatalf("err = %v, want ErrExtendsTarget", err)
	}
	if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("the error does not name the service: %v", err)
	}
	// And it names what IS there, because "no service called nowhere" alone
	// sends the reader hunting.
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("the error does not name the services that exist: %v", err)
	}
}

// An override file may add or change an `extends:`, which is why extends is
// applied AFTER the file chain has merged rather than per file.
func TestExtendsIsAppliedAfterTheFileMerge(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", `
services:
  slim:
    image: alpine
  fat:
    image: ubuntu
    environment:
      SIZE: fat
  web:
    extends: slim
`)
	write(t, dir, "compose.override.yaml", "services:\n  web:\n    extends: fat\n")
	p, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAt(t, p, "services.web.image"); got != "ubuntu" {
		t.Errorf("web image = %q; the override's extends did not win", got)
	}
	if got := scalarAt(t, p, "services.web.environment.SIZE"); got != "fat" {
		t.Errorf("web SIZE = %q", got)
	}
}

// ---- story 1.9: profiles ---------------------------------------------------

func TestProfileMembershipAndTheDeclaredSet(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(`
services:
  always:
    image: alpine
  dev-only:
    image: alpine
    profiles:
      - dev
  both:
    image: alpine
    profiles: [dev, debug]
`), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}

	if !p.AlwaysActive("always") {
		t.Error("a service with no profiles: is not marked always active")
	}
	if p.AlwaysActive("dev-only") {
		t.Error("a service behind a profile is marked always active")
	}
	names, ok := p.ProfilesOf("both")
	if !ok || len(names) != 2 {
		t.Fatalf("ProfilesOf(both) = %v, %v", names, ok)
	}
	// Membership is carried as the positioned values it already is, so a
	// reader can jump to the line that put a service in a profile.
	for _, v := range names {
		if v.Origin().IsZero() {
			t.Errorf("profile %q carries no position", v.Scalar())
		}
	}
	// A service that does not exist is a real "no", distinct from "no profiles".
	if _, ok := p.ProfilesOf("nope"); ok {
		t.Error("ProfilesOf reported an unknown service as present")
	}
	// The declared set, so a caller can offer them without walking services.
	if got := strings.Join(p.DeclaredProfiles(), ","); got != "debug,dev" {
		t.Errorf("DeclaredProfiles = %s, want debug,dev", got)
	}
}

// AD-16: resolve takes no profile argument. Enforced by INSPECTING THE TYPE,
// because a future story adding one would pass code review.
//
// The version this replaces grepped load.go for the literal "Profiles
// []string". It could not fail for the thing it names: adding `Profile string`
// to Options and using it throughout passed, and so did `Profiles []string` the
// moment anyone reformatted the field or moved Options to another file. A grep
// tests the spelling of the source, not the shape of the API.
func TestResolveTakesNoProfileArgument(t *testing.T) {
	opts := reflect.TypeOf(Options{})
	for i := 0; i < opts.NumField(); i++ {
		f := opts.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "profile") {
			t.Errorf("Options.%s selects profiles; filtering belongs to topology (AD-16)", f.Name)
		}
	}

	// And the entry points take Options and nothing else, so a profile cannot
	// arrive beside it either.
	for name, fn := range map[string]any{"Load": Load, "Dir": Dir, "File": File} {
		ft := reflect.TypeOf(fn)
		for i := 0; i < ft.NumIn(); i++ {
			in := ft.In(i)
			if in == reflect.TypeOf(Options{}) || in.Kind() == reflect.String {
				continue
			}
			t.Errorf("%s takes a %s; resolve is a pure function of the file set (AD-16)", name, in)
		}
	}
}

// ---- story 1.10: explain ----------------------------------------------------

func TestExplainReportsTheOriginAndTheOverrideHistory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", "services:\n  web:\n    image: nginx:1.0\n")
	write(t, dir, "compose.override.yaml", "services:\n  web:\n    image: nginx:2.0\n")
	p, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}

	e, err := p.Explain(ParsePath("services.web.image"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(e.Origin().File) != "compose.override.yaml" || e.Origin().Line != 3 {
		t.Errorf("origin = %v", e.Origin())
	}
	hist := e.Overrides()
	if len(hist) != 1 || hist[0].Value != "nginx:1.0" {
		t.Fatalf("override history = %+v", hist)
	}
	if filepath.Base(hist[0].Origin.File) != "compose.yaml" {
		t.Errorf("the replaced value's origin = %v", hist[0].Origin)
	}
}

func TestExplainOfAValueSetOnceHasAnEmptyHistory(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte("services:\n  web:\n    image: nginx\n"), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	e, err := p.Explain(ParsePath("services.web.image"))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Overrides(); got == nil || len(got) != 0 {
		t.Errorf("override history = %v, want present and empty", got)
	}
}

func TestExplainOfAMissingPathNamesTheClosestOnes(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  db:\n    image: postgres\n    restart: always\n  web:\n    image: nginx\n"),
		hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Explain(ParsePath("services.db.imag"))
	if !errors.Is(err, ErrNoSuchPath) {
		t.Fatalf("err = %v, want ErrNoSuchPath", err)
	}
	var pe *PathError
	if !errors.As(err, &pe) {
		t.Fatalf("err is %T, want *PathError", err)
	}
	if len(pe.Closest) == 0 {
		t.Fatal("no suggestions were offered")
	}
	if pe.Closest[0] != "services.db.image" {
		t.Errorf("closest = %v, want services.db.image first — shared prefix beats raw edit distance", pe.Closest)
	}
	if !strings.Contains(err.Error(), "services.db.image") {
		t.Errorf("the message does not carry the suggestion: %v", err)
	}
}

// ---- AD-10 ------------------------------------------------------------------

// The resolver must never invoke the Compose CLI. This is checked mechanically
// rather than by review: the whole product is the claim that resolution is done
// here, and a single exec.Command added in a later story would erase it while
// every test still passed.
func TestResolveNeverShellsOut(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"os/exec", "docker compose", "docker-compose"}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, b := range banned {
			if strings.Contains(string(src), `"`+b+`"`) {
				t.Errorf("%s references %q; AD-10 forbids the CLI in the resolution path", name, b)
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no files — the check is vacuous")
	}

	// The resolver also imports nothing else under internal/, which is what
	// keeps the corpus harness able to exercise the leaf packages without
	// dragging it in.
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		src, _ := os.ReadFile(name)
		if strings.Contains(string(src), "elzouhery/composure/internal/") &&
			!strings.HasSuffix(name, "_test.go") {
			t.Errorf("%s imports another internal package; resolve must stay a leaf", name)
		}
	}
}

// A file repeated in a `-f` chain is a POSITION, not a duplicate.
//
// `docker compose -f base -f override -f base` is legal and used — the third
// entry re-asserts what the second changed. The loader deduped the ordered file
// list by absolute path, so Files(a, b, a) merged three files and reported two,
// and every value the third position set claimed Step 0. AD-15 requires a step
// number to identify a position on its own, and it cannot when two positions
// share one.
func TestRepeatedFileInAChainKeepsItsOwnPosition(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "base.yaml", "services:\n  web:\n    image: base\n")
	over := write(t, dir, "over.yaml", "services:\n  web:\n    image: override\n")

	p, err := Load(Options{Files: []string{base, over, base}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatal(err)
	}

	files := p.Files()
	if len(files) != 3 {
		t.Fatalf("Files() = %+v, want three entries — one per position in the chain", files)
	}
	for i, f := range files {
		if f.Step != i {
			t.Errorf("Files()[%d].Step = %d; a step must be the index of its own entry", i, f.Step)
		}
	}
	if files[0].Path != base || files[1].Path != over || files[2].Path != base {
		t.Errorf("Files() = %+v, want base, over, base in chain order", files)
	}

	// The last position wins, and says so. Before the fix this reported Step 0
	// — the FIRST occurrence — so a consumer resolving the step against the
	// file list was told the wrong entry set the value.
	img, ok := p.At(ParsePath("services.web.image"))
	if !ok {
		t.Fatal("image did not resolve")
	}
	if got := img.Scalar(); got != "base" {
		t.Fatalf("image = %q, want base: the third entry re-asserts the first", got)
	}
	if img.Origin().Step != 2 {
		t.Errorf("image origin step = %d, want 2 — the position that set it", img.Origin().Step)
	}
	if img.Origin().File != base {
		t.Errorf("image origin file = %q, want %q", img.Origin().File, base)
	}
}

// The same file named two ways in an `include:` graph is still ONE step: only
// the explicit chain treats a repeat as a position.
func TestDiscoveredFilesAreStillDedupedByPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "shared.yaml", "services:\n  db:\n    image: postgres\n")
	entry := write(t, dir, "compose.yaml",
		"include:\n  - shared.yaml\n  - ./shared.yaml\nservices:\n  web:\n    image: nginx\n")

	p, err := Load(Options{Files: []string{entry}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.Files()); got != 2 {
		t.Errorf("Files() has %d entries (%+v), want 2: an include reached twice is one file", got, p.Files())
	}
}

// A SECOND `extends:` hop inside a remote file resolves against THAT FILE, not
// against the merged project.
//
// readExtends returned "" for an extends mapping with no `file:`, and "" means
// "the merged project". For the second hop of a chain written in a remote file
// that is wrong in two ways, both silent: a valid project is refused, or — the
// dangerous one — a same-named LOCAL service is found instead and inherited
// from, with the provenance confirming the wrong answer.
//
// The fixture makes the wrong answer VISIBLE rather than merely absent: the
// local project has a service called `core` too, with a different image, so
// resolving against the wrong project succeeds and returns the wrong value.
func TestExtendsSecondHopResolvesInTheFileThatDeclaredIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "remote.yaml", `
services:
  core:
    image: remote-core
    environment:
      FROM_REMOTE_CORE: "yes"
  base:
    extends:
      service: core
    environment:
      FROM_REMOTE_BASE: "yes"
`)
	entry := write(t, dir, "compose.yaml", `
services:
  core:
    image: local-core
  web:
    extends:
      file: remote.yaml
      service: base
`)

	p, err := Load(Options{Files: []string{entry}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("a valid two-hop extends was refused: %v", err)
	}

	if got := scalarAtPath(t, p, "services.web.image"); got != "remote-core" {
		t.Errorf("web.image = %q, want remote-core: the second hop must resolve inside remote.yaml, not against the local project", got)
	}
	if _, ok := p.At(ParsePath("services.web.environment.FROM_REMOTE_CORE")); !ok {
		t.Error("web did not inherit from remote.yaml's core")
	}
	if _, ok := p.At(ParsePath("services.web.environment.FROM_REMOTE_BASE")); !ok {
		t.Error("web did not inherit from remote.yaml's base")
	}

	// The provenance must agree. Inheriting the wrong service and then citing
	// the file it was NOT read from is the failure this project exists to
	// prevent.
	img, _ := p.At(ParsePath("services.web.image"))
	if got := filepath.Base(img.Origin().File); got != "remote.yaml" {
		t.Errorf("web.image origin file = %s, want remote.yaml", got)
	}

	// The local `core` is untouched.
	if got := scalarAtPath(t, p, "services.core.image"); got != "local-core" {
		t.Errorf("local core.image = %q, want local-core", got)
	}
}

// The SHORT form is the same rule.
func TestExtendsShortFormSecondHopResolvesInItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "remote.yaml", "services:\n  core:\n    image: remote-core\n  base:\n    extends: core\n")
	entry := write(t, dir, "compose.yaml",
		"services:\n  core:\n    image: local-core\n  web:\n    extends:\n      file: remote.yaml\n      service: base\n")

	p, err := Load(Options{Files: []string{entry}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("a valid two-hop extends was refused: %v", err)
	}
	if got := scalarAtPath(t, p, "services.web.image"); got != "remote-core" {
		t.Errorf("web.image = %q, want remote-core", got)
	}
}

// An `extends:` with no `file:`, written in a file of the CHAIN, still resolves
// against the merged chain — which is what Compose does and what the top-level
// call's empty file means. The fix must not have moved that.
func TestExtendsWithoutFileStillResolvesAgainstTheMergedChain(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "base.yaml", "services:\n  common:\n    image: base-common\n  web:\n    extends:\n      service: common\n")
	over := write(t, dir, "over.yaml", "services:\n  common:\n    image: overridden-common\n")

	p, err := Load(Options{Files: []string{base, over}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := scalarAtPath(t, p, "services.web.image"); got != "overridden-common" {
		t.Errorf("web.image = %q; a same-file extends resolves against the MERGED project, so the override must be in effect", got)
	}
}

func scalarAtPath(t *testing.T, p *Project, path string) string {
	t.Helper()
	v, ok := p.At(ParsePath(path))
	if !ok {
		t.Fatalf("%s did not resolve", path)
	}
	return v.Scalar()
}

// ---- profiles: the branches nothing exercised -------------------------------

// `profiles: dev` — the scalar spelling. Files write it and Compose accepts it.
//
// The branch that reads it was DEAD in tests: deleting it passed the suite,
// while its own comment says misreading it reports a gated service as always
// active — which is the opposite of what the author asked for and starts a
// service nobody wanted started.
func TestProfilesScalarFormGatesTheService(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  debug:\n    image: busybox\n    profiles: dev\n  web:\n    image: nginx\n"),
		hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}

	names, ok := p.ProfilesOf("debug")
	if !ok {
		t.Fatal("debug did not resolve")
	}
	if len(names) != 1 || names[0].Scalar() != "dev" {
		t.Fatalf("ProfilesOf(debug) = %v, want the one profile the scalar names", names)
	}
	if p.AlwaysActive("debug") {
		t.Error("a service with `profiles: dev` was reported as always active; it is gated")
	}
	if got := strings.Join(p.DeclaredProfiles(), ","); got != "dev" {
		t.Errorf("DeclaredProfiles = %q, want dev: a caller cannot offer a profile it cannot see", got)
	}
	// The service with no profiles at all is still always active.
	if !p.AlwaysActive("web") {
		t.Error("a service with no profiles must be always active")
	}
	// Each name is the positioned value it is in the model, so a consumer can
	// jump to the line that gated the service.
	if names[0].Origin().Line != 4 {
		t.Errorf("profile origin line = %d, want 4", names[0].Origin().Line)
	}
}

// `profiles: [${UNSET}]` puts a service in a profile NOBODY CAN NAME.
//
// DeclaredProfiles skips the empty name because there is nothing to offer;
// AlwaysActive counts it because the author did gate the service. Both are
// right on their own terms, and the service that falls between them cannot be
// started at all — not by default, and not by any `--profile`. Nothing said so.
func TestAProfileNameThatResolvedToNothingIsReported(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  ghost:\n    image: busybox\n    profiles:\n      - ${MISSING_PROFILE}\n"),
		hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}

	// The gap itself, asserted so the finding cannot be "fixed" by quietly
	// changing one of the two views instead.
	if p.AlwaysActive("ghost") {
		t.Error("ghost reported as always active; it declares a profile")
	}
	if got := p.DeclaredProfiles(); len(got) != 0 {
		t.Errorf("DeclaredProfiles = %v, want empty: there is no name to offer", got)
	}

	var found *Finding
	for i, f := range p.Findings() {
		if f.Kind == FindingEmptyProfileName {
			found = &p.Findings()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no %s finding; an unstartable service is exactly the silent wrong answer this engine must not produce", FindingEmptyProfileName)
	}
	if !strings.Contains(found.Message, "ghost") {
		t.Errorf("finding does not name the service: %s", found.Message)
	}
	if found.Origin.IsZero() {
		t.Errorf("finding carries no position: %+v", found.Origin)
	}
	// The usual culprit is named when it can be seen.
	if !strings.Contains(found.Message, "MISSING_PROFILE") {
		t.Errorf("finding does not name the variable that resolved to nothing: %s", found.Message)
	}
}

// A project whose profiles all have names raises nothing. A finding that fires
// on ordinary files is noise.
func TestNamedProfilesRaiseNoEmptyNameFinding(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  web:\n    image: nginx\n    profiles: [dev, debug]\n"), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Findings() {
		if f.Kind == FindingEmptyProfileName {
			t.Errorf("unexpected finding: %s", f.Message)
		}
	}
}
