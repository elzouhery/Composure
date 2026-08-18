// Command composure is the headless surface of the engine.
//
// Every capability lands here before any UI binds to it. That is not a testing
// convenience: the corpus harness can only exercise headless code, and
// corpus-scale testing is what makes this engine trustworthy. It is also a
// product requirement — the core has to be scriptable and CI-usable — and it
// is the process the VS Code extension speaks to.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elzouhery/composure/internal/edit"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "resolve":
		fs := flag.NewFlagSet("resolve", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		var chain fileChain
		fs.Var(&chain, "f", "a compose file; repeatable, merged left to right. Disables the override pickup")
		// Flags must precede the positional path: flag parsing stops at the
		// first non-flag argument.
		_ = fs.Parse(os.Args[2:])
		project, entry := openProject(fs, "resolve", chain, *asJSON)
		runResolve(project, entry, *asJSON)
	case "topology":
		fs := flag.NewFlagSet("topology", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		var profiles profileList
		var chain fileChain
		fs.Var(&profiles, "profile", "activate a profile; repeatable, or comma-separated")
		fs.Var(&chain, "f", "a compose file; repeatable, merged left to right. Disables the override pickup")
		_ = fs.Parse(os.Args[2:])
		project, entry := openProject(fs, "topology", chain, *asJSON)
		runTopology(project, entry, profiles, *asJSON)
	case "diagnose":
		fs := flag.NewFlagSet("diagnose", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		var profiles profileList
		var chain fileChain
		fs.Var(&profiles, "profile", "activate a profile; repeatable, or comma-separated")
		fs.Var(&chain, "f", "a compose file; repeatable, merged left to right. Disables the override pickup")
		_ = fs.Parse(os.Args[2:])
		project, entry := openProject(fs, "diagnose", chain, *asJSON)
		runDiagnose(project, entry, profiles, *asJSON)
	case "explain":
		fs := flag.NewFlagSet("explain", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		var chain fileChain
		fs.Var(&chain, "f", "a compose file; repeatable, merged left to right. Disables the override pickup")
		_ = fs.Parse(os.Args[2:])
		at := fs.Arg(0)
		if strings.TrimSpace(at) == "" {
			fmt.Fprint(os.Stderr, "composure: explain needs a config path, e.g. services.web.ports[0]\n\n")
			usage()
		}
		// explain takes TWO positionals — the config path, then optionally the
		// project — so it cannot share positionalPath's one-argument rule.
		project, entry := openProjectAt(fs, "explain", chain, 1, *asJSON)
		runExplain(project, entry, at, *asJSON)
	case "impact":
		fs := flag.NewFlagSet("impact", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		var profiles profileList
		var chain fileChain
		fs.Var(&profiles, "profile", "activate a profile; repeatable, or comma-separated")
		fs.Var(&chain, "f", "a compose file; repeatable, merged left to right. Disables the override pickup")
		_ = fs.Parse(os.Args[2:])
		at := fs.Arg(0)
		if strings.TrimSpace(at) == "" {
			fmt.Fprint(os.Stderr, "composure: impact needs a config path, e.g. services.db\n\n")
			usage()
		}
		// Two positionals, like explain: the config path, then optionally the
		// project.
		project, entry := openProjectAt(fs, "impact", chain, 1, *asJSON)
		runImpact(project, entry, at, profiles, *asJSON)
	case "schema":
		fs := flag.NewFlagSet("schema", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		at := fs.String("at", "", "describe one config path, e.g. services.db; default is every node")
		_ = fs.Parse(os.Args[2:])
		runSchema(positionalPath(fs, "schema"), *at, *asJSON)
	case "dockerfile":
		fs := flag.NewFlagSet("dockerfile", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		at := fs.String("at", "", "a build section's config path in a compose file, e.g. services.docs.build")
		_ = fs.Parse(os.Args[2:])
		runDockerfile(dockerfilePath(fs, *at), *at, *asJSON)
	case "preview", "apply":
		// One flag set, one code path, one boolean apart. A preview that took a
		// different route from the write would be showing a diff nobody can
		// hold the write to.
		write := os.Args[1] == "apply"
		fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		var f editFlags
		fs.StringVar(&f.op, "op", "", "replace_scalar | insert_key | insert_sequence_entry | delete_key | set_comment | delete_comment | set_base_image | replace_args | insert_instruction | insert_instruction_before | insert_stage")
		fs.StringVar(&f.at, "at", "", "config path; for insert_key the MAPPING the key is added to")
		fs.StringVar(&f.key, "key", "", "the key insert_key adds, or the AS name insert_stage gives the stage")
		fs.StringVar(&f.value, "value", "", "the new scalar, inserted value, comment text, instruction text, or image reference")
		fs.StringVar(&f.where, "where", "", "for set_comment and delete_comment: above | trailing")
		fs.IntVar(&f.stage, "stage", 0, "zero-based FROM index, for set_base_image and insert_instruction")
		fs.IntVar(&f.instruction, "instruction", 0, "zero-based instruction index, for replace_args")
		expectStart := fs.Int("expect-start", -1, "assert the target starts at this byte; refuse if it moved")
		expectEnd := fs.Int("expect-end", -1, "assert the target ends at this byte")
		fs.StringVar(&f.expectText, "expect-text", "", "assert the target currently reads exactly this")
		_ = fs.Parse(os.Args[2:])
		if strings.TrimSpace(f.op) == "" {
			fmt.Fprintf(os.Stderr, "composure: %s needs -op\n\n", os.Args[1])
			usage()
		}
		// -expect-text alone counts. It used to be ignored unless one of the
		// two offsets was passed as well, so a caller who asked "write this
		// only if the target still reads X" was answered as though they had
		// asked for no guard at all — a staleness check that silently does
		// nothing, which is the failure shape this engine is built against.
		if *expectStart >= 0 || *expectEnd >= 0 || f.expectText != "" {
			f.hasExpect = true
			f.expectStart, f.expectEnd = *expectStart, *expectEnd
		}
		runEdit(editPath(fs, os.Args[1], f.op), f, write, *asJSON)
	case "editable":
		// The read side of the write path: where a value is WRITTEN, which is
		// not always the path it is read from. `-at` is required because the
		// question is about one path.
		fs := flag.NewFlagSet("editable", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		at := fs.String("at", "", "the config path to ask about, e.g. services.web.restart")
		_ = fs.Parse(os.Args[2:])
		runEditable(positionalPath(fs, "editable"), *at, *asJSON)
	case "add":
		// Stories 7.3 and 7.4. A planner in front of preview/apply, never a
		// second write path: `add` builds the operations and hands them to the
		// same two functions `preview` and `apply` call.
		fs := flag.NewFlagSet("add", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		write := fs.Bool("write", false, "write it. Without this the declaration is previewed and nothing is written")
		var chain fileChain
		fs.Var(&chain, "f", "a compose file; repeatable, merged left to right. The FIRST is written to")
		var f addFlags
		fs.StringVar(&f.kind, "kind", "", "service | network | volume | config | secret")
		fs.StringVar(&f.name, "name", "", "the name to declare, exactly as it goes in the file")
		fs.StringVar(&f.value, "value", "", "a service's image. A resource takes none")
		_ = fs.Parse(os.Args[2:])
		if strings.TrimSpace(f.kind) == "" {
			fmt.Fprint(os.Stderr, "composure: add needs -kind (service | network | volume | config | secret)\n\n")
			usage()
		}
		arg := "."
		if fs.NArg() > 0 {
			arg = fs.Arg(0)
		}
		target := positionalPath(fs, "add")
		if len(chain) > 0 {
			// The chain resolves the project the duplicate check runs against;
			// the declaration itself lands in the file the reader named first,
			// because a request that spans three files produces three diffs and
			// one button.
			target = chain[0]
		}
		runAdd(target, addProject(chain, arg), f, *write, *asJSON)
	case "extract", "extract-write":
		// Story 9.3. The one command that writes TWO files, which is why it is
		// its own subcommand and not an -op: an edit.Request is one file, and
		// here the second is derived from the first rather than named.
		fs := flag.NewFlagSet("extract", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		write := fs.Bool("write", false, "write it. Without this both diffs are previewed and NEITHER file is written")
		var f extractFlags
		fs.StringVar(&f.at, "at", "", "the config path of the literal to move, e.g. services.db.environment.POSTGRES_PASSWORD")
		fs.StringVar(&f.name, "name", "", "the variable's name. Empty derives one from the key, or from the image for a FROM tag")
		fs.StringVar(&f.envFile, "env", "", "the .env to write. Empty is the .env beside the compose file, which is the only one Compose interpolates from")
		// Story 9.4. A Dockerfile is addressed by instruction index, not by a
		// config path: there are no paths in that grammar.
		fs.IntVar(&f.instruction, "instruction", -1, "a Dockerfile's zero-based instruction index, for moving a value into an ARG")
		fs.StringVar(&f.part, "part", "", "which part of the instruction moves. For a FROM: tag, which is also the default")
		// Story 9.6. Optional on purpose — see extractFlags.
		expectStart := fs.Int("expect-start", -1, "assert the value starts at this byte; refuse if it moved")
		expectEnd := fs.Int("expect-end", -1, "assert the value ends at this byte")
		fs.StringVar(&f.expectText, "expect-text", "", "assert the value currently reads exactly this")
		expectEnvSet := fs.Bool("expect-env-defined", false, "assert the .env DID define the name when this was previewed")
		expectEnvValue := fs.String("expect-env-value", "", "with -expect-env-defined: the value it held")
		_ = fs.Parse(os.Args[2:])
		// -expect-text alone counts here for the same reason it counts for
		// preview and apply: a caller asking "move this only if it still reads
		// X" is asking for a guard, and answering as though they asked for none
		// is worse than having no flag. This call site was missed when the
		// other was fixed, on the ONE operation that writes two files.
		if *expectStart >= 0 || *expectEnd >= 0 || f.expectText != "" {
			f.hasExpect = true
			f.expectStart, f.expectEnd = *expectStart, *expectEnd
		}
		// Visit, not the value: `-expect-env-defined=false` is the assertion
		// "it was NOT set", which is a different request from sending nothing
		// at all. A bool read by value cannot tell the two apart.
		fs.Visit(func(fl *flag.Flag) {
			if fl.Name == "expect-env-defined" || fl.Name == "expect-env-value" {
				f.hasExpectEnv = true
			}
		})
		f.expectEnvSet, f.expectEnvValue = *expectEnvSet, *expectEnvValue
		runExtract(positionalPath(fs, "extract"), f, *write, *asJSON)
	case "image":
		// Epic 8. The one subcommand in this product that reaches the network;
		// everything about it is arranged so that a machine with no egress gets
		// exactly the tool a machine with egress gets, minus the tag list.
		os.Exit(runImage(os.Args[2:]))
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "composure: serve takes no arguments, got %q\n", fs.Arg(0))
			os.Exit(2)
		}
		// stdout is the protocol and nothing else; diagnostics go to stderr.
		os.Exit(serve(os.Stdin, os.Stdout, os.Stderr))
	case "-h", "--help", "help":
		// An explicitly requested help is not an error.
		fmt.Print(usageText)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "composure: unknown command %q\n\n", os.Args[1])
		usage()
	}
}

// profileList collects a repeatable -profile flag. Comma-separated values are
// accepted in one flag too, because that is what every shell script reaches for
// and silently treating "dev,prod" as a single profile name would filter the
// whole stack away without saying why.
type profileList []string

func (p *profileList) String() string { return strings.Join(*p, ",") }

func (p *profileList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*p = append(*p, part)
		}
	}
	return nil
}

// fileChain collects a repeatable -f flag: an explicit chain of compose files,
// merged left to right (story 1.6).
//
// It is deliberately NOT comma-split the way -profile is. A path may contain a
// comma, and silently turning one file into two would produce a "file not
// found" naming half a real path.
type fileChain []string

func (f *fileChain) String() string { return strings.Join(*f, " -f ") }

func (f *fileChain) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("empty -f path")
	}
	*f = append(*f, v)
	return nil
}

// openProject resolves whatever the argument grammar named, applying the same
// rules to every subcommand:
//
//   - an explicit -f chain merges left to right and DISABLES the automatic
//     compose.override.yaml pickup, which is what Compose does;
//   - a directory, or no argument at all, gets the Compose candidate order
//     plus its override file;
//   - a single file named positionally is that file alone, with no override
//     pickup — naming a file is as explicit as -f.
//
// It returns the project and the entry path to display and to locate a `.env`
// from. It never returns on failure.
func openProject(fs *flag.FlagSet, cmd string, chain fileChain, asJSON bool) (*resolve.Project, string) {
	return openProjectAt(fs, cmd, chain, 0, asJSON)
}

// openProjectAt is openProject for a command whose project argument is not the
// first positional one — `explain` takes the config path first.
func openProjectAt(fs *flag.FlagSet, cmd string, chain fileChain, argIndex int, asJSON bool) (*resolve.Project, string) {
	if len(chain) > 0 {
		if fs.NArg() > argIndex {
			fmt.Fprintf(os.Stderr,
				"composure: %s takes either -f files or a path, not both (got %q after %d -f flags)\n",
				cmd, fs.Arg(argIndex), len(chain))
			os.Exit(2)
		}
		project, err := resolve.Files(chain...)
		if err != nil {
			reportResolveFailure(chain[0], err, asJSON)
		}
		return project, chain[0]
	}

	if fs.NArg() > argIndex+1 {
		fmt.Fprintf(os.Stderr,
			"composure: unexpected argument %q after the path.\nFlags must come first: composure %s -json %s\n",
			fs.Arg(argIndex+1), cmd, fs.Arg(argIndex))
		os.Exit(2)
	}

	arg := "."
	if fs.NArg() > argIndex {
		arg = fs.Arg(argIndex)
	}
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		project, err := resolve.Dir(arg)
		if err != nil {
			reportResolveFailure(filepath.Join(arg, "compose.yaml"), err, asJSON)
		}
		return project, project.Files()[0].Path
	}
	project, err := resolve.File(arg)
	if err != nil {
		reportResolveFailure(arg, err, asJSON)
	}
	return project, arg
}

// positionalPath applies the shared argument rules: flags first, at most one
// path, and Compose's own candidate order when none is given.
func positionalPath(fs *flag.FlagSet, cmd string) string {
	// Anything left after the path is either a flag that arrived too late to
	// be parsed — `resolve compose.yml -json` silently printed a table — or an
	// argument we would discard. Both are silent wrong behaviour.
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr,
			"composure: unexpected argument %q after the path.\nFlags must come first: composure %s -json %s\n",
			fs.Arg(1), cmd, fs.Arg(0))
		os.Exit(2)
	}
	dir := "."
	if fs.NArg() > 0 {
		arg := fs.Arg(0)
		// A directory is the documented way to name a project — `composure
		// topology -json .` — so it is expanded rather than handed to the
		// resolver, which would report "is a directory" and be right but
		// useless.
		if info, err := os.Stat(arg); err != nil || !info.IsDir() {
			return arg
		}
		dir = arg
	}
	// Compose's own candidate order, so `composure resolve` in a project
	// directory finds what `docker compose` would.
	for _, c := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
			return filepath.Join(dir, c)
		}
	}
	return filepath.Join(dir, "compose.yaml")
}

// dockerfilePath applies the argument rules for `composure dockerfile`.
//
// With -at the positional argument names a COMPOSE file, so it gets the Compose
// candidate order every other subcommand uses. Without it the argument names a
// Dockerfile, and a directory means the file called `Dockerfile` inside it.
func dockerfilePath(fs *flag.FlagSet, at string) string {
	if strings.TrimSpace(at) != "" {
		return positionalPath(fs, "dockerfile")
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr,
			"composure: unexpected argument %q after the path.\nFlags must come first: composure dockerfile -json %s\n",
			fs.Arg(1), fs.Arg(0))
		os.Exit(2)
	}
	arg := "."
	if fs.NArg() > 0 {
		arg = fs.Arg(0)
	}
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		return filepath.Join(arg, "Dockerfile")
	}
	return arg
}

// editPath applies the argument rules for `composure preview` and `composure apply`.
//
// A Dockerfile operation names the Dockerfile; everything else names a compose
// file and gets the Compose candidate order. The operation decides, because the
// operation already decides which engine performs the edit.
func editPath(fs *flag.FlagSet, cmd, op string) string {
	if edit.Operation(op).Grammar() == "dockerfile" {
		if fs.NArg() != 1 {
			fmt.Fprintf(os.Stderr, "composure: %s -op %s needs exactly one path, the Dockerfile\n", cmd, op)
			os.Exit(2)
		}
		return fs.Arg(0)
	}
	return positionalPath(fs, cmd)
}

func runResolve(project *resolve.Project, path string, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(project); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}

	files := project.Files()
	fmt.Printf("\nRESOLVED — %s\n", path)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("source files: %d\n", len(files))
	for _, f := range files {
		fmt.Printf("  [%d] %s\n", f.Step, f.Path)
	}

	section := func(label string, m *resolve.OrderedMap) {
		if m.Len() == 0 {
			return
		}
		fmt.Printf("\n%s (%d)\n", label, m.Len())
		for _, k := range m.Keys() {
			v, _ := m.Get(k)
			fmt.Printf("  %-28s %s\n", k, v.Origin())
		}
	}
	section("SERVICES", project.Services())
	section("NETWORKS", project.Networks())
	section("VOLUMES", project.Volumes())
	section("CONFIGS", project.Configs())
	section("SECRETS", project.Secrets())

	if ext := project.Extensions(); len(ext) > 0 {
		fmt.Printf("\nEXTENSIONS (%d)\n", len(ext))
		for _, k := range ext {
			fmt.Printf("  %s\n", k)
		}
	}

	printDroppedInMerge(project)

	// The headline number for this story: provenance coverage. Anything below
	// 100% means a value reached the model without knowing where it came from,
	// which is the one thing this package exists to prevent.
	var leaves, withOrigin int
	project.Walk(func(_ resolve.Path, v *resolve.Value) bool {
		if v.Kind() == resolve.KindScalar || v.Kind() == resolve.KindNull {
			leaves++
			if !v.Origin().IsZero() {
				withOrigin++
			}
		}
		return true
	})
	fmt.Printf("\nprovenance: %d/%d leaf values carry an origin\n\n", withOrigin, leaves)
}

// printDroppedInMerge reports the configuration the merge did not keep.
//
// A key written as a list in one file and a mapping in another is replaced
// whole rather than normalised — the resolved model keeps the shape each file
// wrote, because the splice engine edits those bytes — so entries only the
// earlier form set are gone. `docker compose config` keeps them, so this is a
// real divergence and the reader has to be told about it.
//
// It used to appear only in `-json`, which meant that for everyone reading the
// table configuration vanished with no signal at all. CLAUDE.md rule 6: an
// operation that cannot be performed safely says so. Nothing is printed when
// there is nothing to report — a permanently present empty section trains
// people to skip it.
func printDroppedInMerge(project *resolve.Project) {
	var dropped []resolve.Finding
	for _, f := range project.Findings() {
		if f.Kind == resolve.FindingFormMismatch {
			dropped = append(dropped, f)
		}
	}
	if len(dropped) == 0 {
		return
	}
	fmt.Printf("\nDROPPED IN MERGE (%d)  <- configuration you wrote that is NOT in the resolved model\n", len(dropped))
	for _, f := range dropped {
		fmt.Printf("  %s\n", f.Path)
		if len(f.Dropped) > 0 {
			fmt.Printf("      lost:  %s\n", strings.Join(f.Dropped, ", "))
		} else {
			fmt.Printf("      lost:  entries only the earlier declaration set\n")
		}
		if !f.Displaced.IsZero() {
			fmt.Printf("      from:  %s\n", f.Displaced)
		}
		fmt.Printf("      kept:  %s\n", f.Origin)
		fmt.Printf("      why:   the two files write this key in different shapes, and reconciling them would mean\n")
		fmt.Printf("             re-emitting the collection — so the later declaration replaces the earlier whole.\n")
		fmt.Printf("             `docker compose config` normalises the forms and keeps both. Run `composure diagnose`.\n")
	}
}

// runTopology derives the graph from the resolved model and prints it. It
// resolves once and filters by profile afterwards (AD-16): asking for a second
// profile set never re-reads a file.
func runTopology(project *resolve.Project, path string, profiles []string, asJSON bool) {
	graph, err := topology.Build(project, profiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "composure: %v\n", err)
		os.Exit(1)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(graph); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printTopology(path, graph)
}

func printTopology(path string, g *topology.Graph) {
	fmt.Printf("\nTOPOLOGY — %s\n", path)
	fmt.Println(strings.Repeat("=", 78))
	if p := g.Profiles(); len(p) > 0 {
		fmt.Printf("active profiles: %s\n", strings.Join(p, ", "))
	} else {
		fmt.Println("active profiles: none (only always-active services)")
	}

	nodes := g.Nodes()
	byKind := map[topology.NodeKind][]topology.Node{}
	var order []topology.NodeKind
	for _, n := range nodes {
		if _, seen := byKind[n.Kind]; !seen {
			order = append(order, n.Kind)
		}
		byKind[n.Kind] = append(byKind[n.Kind], n)
	}
	for _, kind := range order {
		rows := byKind[kind]
		fmt.Printf("\n%sS (%d)\n", strings.ToUpper(string(kind)), len(rows))
		for _, n := range rows {
			var tags []string
			if !n.Declared {
				tags = append(tags, "undeclared")
			}
			if n.External {
				tags = append(tags, "external")
			}
			if n.Internal {
				tags = append(tags, "internal")
			}
			if len(n.Profiles) > 0 {
				tags = append(tags, "profiles="+strings.Join(n.Profiles, "|"))
			}
			label := ""
			if len(tags) > 0 {
				label = "  [" + strings.Join(tags, " ") + "]"
			}
			fmt.Printf("  L%-2d %-32s %s%s\n", n.Layer, n.Name, n.Origin, label)
		}
	}

	edges := g.Edges()
	fmt.Printf("\nEDGES (%d)\n", len(edges))
	for _, e := range edges {
		fmt.Printf("  %-12s %-28s -> %-28s %s%s\n",
			e.Kind, e.From.String(), e.To.String(), e.Origin, edgeDetail(e))
	}

	if cycles := g.Cycles(); len(cycles) > 0 {
		fmt.Printf("\nCYCLES (%d)\n", len(cycles))
		for _, c := range cycles {
			var names []string
			for _, p := range c {
				names = append(names, p.String())
			}
			fmt.Printf("  %s\n", strings.Join(names, " -> "))
		}
	}

	if d := g.Dangling(); len(d) > 0 {
		fmt.Printf("\nDANGLING (%d)\n", len(d))
		for _, x := range d {
			fmt.Printf("  %-12s %-28s -> %-20s %s (%s)\n", x.Kind, x.From.String(), x.Ref, x.Origin, x.Reason)
		}
	}

	fmt.Printf("\n%d nodes, %d edges, %d layers\n\n", len(nodes), len(edges), g.MaxLayer()+1)
}

func edgeDetail(e topology.Edge) string {
	switch {
	case e.Depends != nil:
		out := "  " + e.Depends.Condition
		if e.Depends.Restart != "" {
			out += " restart=" + e.Depends.Restart
		}
		if e.Depends.Required != "" {
			out += " required=" + e.Depends.Required
		}
		return out
	case e.Attach != nil && len(e.Attach.Aliases) > 0:
		return "  aliases=" + strings.Join(e.Attach.Aliases, ",")
	case e.Mount != nil:
		return fmt.Sprintf("  %s:%s:%s", e.Mount.Source, e.Mount.Target, e.Mount.Mode)
	case e.Port != nil:
		return "  " + e.Port.Raw
	case e.Build != nil:
		if e.Build.Inline {
			return "  dockerfile_inline"
		}
		out := "  " + e.Build.Reference
		if e.Build.Target != "" {
			out += " target=" + e.Build.Target
		}
		return out
	}
	return ""
}

// reportResolveFailure prints a resolve error in whichever shape the caller
// asked for and exits. It never returns.
func reportResolveFailure(path string, err error, asJSON bool) {
	if asJSON {
		// The extension speaks this protocol. A ParseError carries the line
		// and column precisely so a caller can place the cursor — printing
		// prose to stderr throws that away.
		env := map[string]any{"error": err.Error(), "file": path}
		var pe *resolve.ParseError
		if errors.As(err, &pe) && pe.Line > 0 {
			env["line"], env["column"] = pe.Line, pe.Column
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(env)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "composure: %v\n", err)
	os.Exit(1)
}

const usageText = `composure — read, understand and safely edit container configuration

  composure resolve  [-json] [-f file...] <path> resolve a project, with provenance
  composure explain  [-json] <config-path> <path> which file set a value, and what it overrode
  composure topology [-json] [-profile p] <path> the graph of what talks to what
  composure impact   [-json] [-profile p] <config-path> <path> what breaks if a node goes down
  composure diagnose [-json] [-profile p] <path> what is wrong with the stack
  composure schema   [-json] [-at path] <path>   what you set, and what you could set
  composure dockerfile [-json] [-at p] <path>    a Dockerfile as stages and instructions
  composure editable [-json] -at ... <path>      whether a value can be edited in place, and why not
  composure preview  [-json] -op ... <path>      the diff an edit would produce. Writes nothing
  composure apply    [-json] -op ... <path>      the same edit, written through the splice engine
  composure add      [-json] [-write] -kind ... <path>  declare a service, network, volume, config or secret
  composure extract  [-json] [-write] -at ... <path>    move a literal into a variable, and into the .env
  composure extract  [-json] [-write] -instruction N <Dockerfile>  move a literal into a build argument
  composure image lookup [-json] <reference>     what an image is, and what is newer
  composure image search [-json] <query>         find an image on Docker Hub by name
  composure image stale  [-json] <Dockerfile>    every stage's base image, and the file's age
  composure serve                              JSON-RPC 2.0 over stdio, for the editor

Flags precede the positional path. A directory is expanded with the Compose
candidate order: compose.yaml, compose.yml, docker-compose.yaml,
docker-compose.yml, and its compose.override.yaml is picked up automatically.
With no path at all, the current directory is used.

-f names an explicit chain, repeatable, merged left to right by the Compose
merge rules — mappings key-wise, sequences appended, command, entrypoint and
healthcheck.test replaced, ports, volumes, secrets and configs merged on their
uniqueness key, !reset removing a declaration and !override bypassing the rule.
Passing -f DISABLES the automatic override pickup, which is what Compose does.
A file in the chain that is not there is an error naming it, and nothing
resolves. include: is followed recursively with every path taken relative to
the file that declared it, and extends: is applied afterwards — across files
too. Variables are interpolated per file, at load, from .env, every env_file
and the environment: ${VAR}, ${VAR:-default}, ${VAR-default}, ${VAR:+alt},
${VAR:?err} and $$ for a literal dollar. A variable nothing defines resolves to
empty and becomes a finding; ${VAR:?msg} fails with the author's message,
because that form exists in order to fail.

explain answers the question the tool is for: which file, line and column set a
value, and the ordered list of what it replaced, each with its own origin. A
value set once reports an explicitly empty override history. A path that
addresses nothing is an error naming the closest paths that do:

  composure explain services.web.ports[0] .
  composure explain -json services.db.image examples/webstack/compose.yaml

topology derives the graph from the resolved model — nodes for services,
networks, volumes, configs, secrets and published ports, and edges for
depends_on, network attachment, mounts, links, network_mode and publishing.
-profile is repeatable and also accepts a comma-separated list; with none, only
always-active services are included, which is what Compose does.

impact answers "what breaks if this goes down, and what does it need" for one
node, over depends_on edges only — a shared network is a connection, not a
dependency. A path that is not a node in the graph is an error naming it, never
an empty answer. It is what the editor's focus mode dims by.

  composure impact services.db examples/webstack/compose.yaml
  composure impact -json services.api .

diagnose runs the resolved model and its topology past every registered rule.
Findings are never errors: a file that will not parse exits 1, a clean stack
exits 0, and a stack with findings exits by the highest severity present —
10 for hints, 20 for warnings, 30 for errors — so the command works as a CI
gate. Nothing is written to disk; a described fix is data saying what edit
would land where.

schema lists, per config path, every key the Compose specification permits
there and whether the file declares it. The list is GENERATED from the schema
vendored at schema/compose-spec.json (AD-20); no list of Compose keys is
written down anywhere in this codebase. The file's own version: field is
ignored exactly as Compose ignores it — there is one current schema — and
reported as a hint by diagnose instead. Where a key's minimum Compose is known
and this machine has an older one, the key is MARKED and still listed; with no
docker compose on the machine nothing is marked at all.

dockerfile reads a Dockerfile into one group per build stage, instructions in
order, with the byte range of every image reference. With -at it takes a COMPOSE
file and a build section's config path instead, and resolves the Dockerfile
relative to the build context the way Compose reads it. A build naming a file
that is not there returns a form marked missing — never nothing.

preview and apply are the write path, and they are the same code one boolean
apart: the diff preview prints is the diff apply writes.

  -op replace_scalar        -at services.web.image -value nginx:1.27
  -op insert_key            -at services.db -key restart -value always
  -op insert_key            -at "" -key networks                (the document root)
  -op insert_sequence_entry -at services.web.ports -value "9090:90"
  -op delete_key            -at services.web.ports
  -op set_base_image        -stage 0 -value golang:1.24-alpine  (a Dockerfile path)
  -op replace_args          -instruction 4 -value "npm ci"      (a Dockerfile path)
  -op insert_instruction    -stage 1 -value "USER app"          (a Dockerfile path)
  -op insert_stage          -value nginx:1.27 -key serve        (a Dockerfile path)

insert_instruction appends to the end of the stage -stage names, because order
is semantic in that grammar; insert_stage appends a FROM after the file's last
instruction, with -key as its AS name and no AS clause when -key is empty. The
keyword takes the casing the file already uses.

Bytes are patched in place: the engine never re-serialises, so comments, quoting
style, key order, blank lines, line endings and a BOM are untouched by
construction. Changing one scalar is a two-line diff.

add declares something that is not in the file yet, and it is preview and apply
with a planner in front: the reader names a kind and a name, and the operations
that go to the splice engine are ordinary insert_key calls applied as ONE edit.

set_comment and delete_comment take -where above or -where trailing. above is
the run of comment lines directly over the key, at the key's own indent, and a
run is ONE comment: a two-line -value writes two lines and replaces the whole
run. trailing is the comment after the value on the key's own line, found from
the END of the value rather than from the first # on the line. A block scalar,
an anchored or aliased value and a flow collection are refused rather than
guessed at, and deleting a comment that is not there is refused rather than
reported as a delete that removed nothing.

A list entry is addressed by index — services.web.healthcheck.test[1] — and an
index the list does not have is refused by name, with the length in the
sentence. An index moves when the list does, and the answer to that is the
staleness assertion (-expect-start / -expect-text), never a rebase.

extract moves a literal into a variable: the value leaves the compose file as
${NAME} and arrives in the .env beside it, created if it is not there. It is
the ONLY command that changes two files, so it is not an -op: both diffs are
shown, and without -write neither file is written. The .env is the one Compose
interpolates from — never an env_file, which Compose does not consult for
interpolation, so a value written there would resolve to nothing. A name the
.env already sets to something else is refused naming both values; the same
value is not a conflict and the .env is left byte-identical. A Dockerfile is
refused: the equivalent there is ARG, which is build-time and is never fed by a
.env: that is the -instruction form below, on the same subcommand.

On a DOCKERFILE the same subcommand does the other operation, chosen by the
file's own grammar rather than by which flag was typed: -instruction N moves a
literal into an ARG, and it writes ONE file. FROM node:18 becomes
FROM node:${NODE_VERSION} with ARG NODE_VERSION=18 above the FIRST FROM,
because a FROM can only use an ARG declared before the first one; a value
inside a stage gets its declaration directly above its own instruction, inside
that stage, because an ARG used before it is declared expands to the empty
string with no error. A global ARG the stage cannot see is pulled in with a
bare ARG NAME, which is Docker's own way of doing it. The literal stays in the
file as the ARG's default and is never written to a .env: docker compose passes
build arguments only through build.args, so a .env line there would look like
configuration and be inert. build.args is deliberately NOT wired, and the
result says so and names what to write. A value that cannot be written as a
BARE default -- a space, a quote, a #, a backslash or a $ -- is refused rather
than quoted on a guess, because an ARG default has no second reader to check
the quoting against.

Both halves take a staleness assertion. -expect-start / -expect-end /
-expect-text assert the byte range, as everywhere else; -expect-env-defined and
-expect-env-value assert what the .env said about the NAME, which is what
staleness means for a file whose edit is one appended line. Either half moving
refuses the whole thing and writes NEITHER file, and the refusal names which
one moved. A .env that gained the name with exactly the value being moved is
NOT stale: that is the half-landed write this operation's own recovery
converges on.

  composure add -kind service -name cache -value redis:7 .   (a preview)
  composure add -write -kind service -name cache -value redis:7 .
  composure add -write -kind network -name frontend .
  composure add -write -kind volume  -name pgdata .
  composure add -write -kind config  -name nginx .
  composure add -write -kind secret  -name api-key .

A service is two operations — the name, then its image — and a resource is one,
or two when its top-level block is not in the file yet. Either way it is one
request, one diff and one undo: a 'cache:' with nothing under it is not a stack
anything can run, and leaving one on disk because a second call failed is the
partial write the write path exists to make impossible. A resource is written as
'frontend:' with no trailing space and no invented body — a default driver is a
scaffold, and scaffolds are not this.

The name goes after the last thing already in its block, at the indentation the
file already uses. What the reader types is what the file gets: nothing is
quoted, unquoted or re-quoted for them, and a value YAML would read back as
something else — '3.10' is the float 3.1, 'yes' is a boolean to half the parsers
in the world — is REFUSED with the character named, because quoting it for the
reader is a formatting opinion and guessing what they meant is worse. Quote it
yourself and it goes in byte for byte. A name the configuration already declares
is refused too, naming the file and the line that declares it, rather than
writing a duplicate key for YAML to silently discard.

-expect-start, -expect-end and -expect-text assert where the target WAS when the
edit was decided on. If it has moved, the edit is refused rather than rebased —
writing a stale byte range is how a fidelity engine damages a file. An edit the
engine cannot perform safely is refused too, naming what it could not do: a
flow-style collection cannot take a block child, and a multi-line instruction
cannot be rewritten without a line-break policy. Both exit 3, and neither writes.

image is the only part of this tool that reaches the network, and it is built so
that a machine with no egress gets the same product minus one fact. A lookup
answers with a STATE — ok, current, offline, rate-limited, not-found,
other-registry, not-comparable, disabled — and a sentence for a reader, never an
error string, and it EXITS 0 whichever it is: being offline is not a script
failure. Every call is bounded by -timeout, five seconds by default, on a real
context, so a hung socket cannot outlive the question.

Only Docker Hub. A ghcr.io, quay.io or localhost:5000 reference is answered with
a sentence saying so rather than with an empty result, because searching across
registries is deliberately not built and an empty answer reads as "there is
nothing newer". A reference built from a variable, 'scratch', and a digest pin
are each named as what they are: there is no tag to compare, which is not the
same as there being nothing to say.

A candidate offered as an upgrade is in the same family as the tag it replaces,
is stable, is not a date stamp, and is a strictly HIGHER version — never merely
pushed more recently. 'alpine:edge' and 'golang:tip-alpine3.24' are rolling
builds and are strictly worse than the pin they would replace.

There is no vulnerability facet and there will not be one: Docker Hub's public
API does not carry that data.

  composure image lookup postgres:16-alpine
  composure image search -json postgres
  composure image stale examples/webstack/Dockerfile

COMPOSURE_OFFLINE=1 switches every request off before the transport is entered, so
the tool is local again with one variable.

serve speaks JSON-RPC 2.0 with LSP-style Content-Length framing on stdin and
stdout. Methods: initialize, stack/resolve {path}, stack/topology {path,
profiles}, stack/impact {path, profiles, at}, stack/diagnose {path, profiles},
stack/schema {path, at, all},
stack/dockerfile {path, at}, stack/preview {file, ops}, stack/apply {file, ops},
stack/add {file, path, kind, name, value},
image/lookup {ref}, image/search {query, limit},
stack/explain {path, files, at}, shutdown, exit. stack/resolve returns exactly
what resolve -json prints, stack/explain exactly what explain -json prints,
stack/topology exactly what topology -json prints, stack/diagnose exactly what
diagnose -json prints, stack/schema exactly what schema -json prints and
stack/dockerfile exactly what dockerfile -json prints. stack/preview and
stack/apply are preview and apply, and stack/preview writes nothing. stack/add
is 'composure add' without the write: it returns the operations that perform the
declaration and touches nothing, so a client can hold them with whatever else
the reader has staged and send the lot as one apply.
stdout carries the protocol only — every diagnostic goes to stderr.
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
	os.Exit(2)
}
