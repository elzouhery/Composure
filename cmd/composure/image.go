package main

// `composure image` — Docker Hub lookup and search, headless.
//
// CLI BEFORE UI, always. Every capability the extension's pill and search popup
// use is here first, and both surfaces call the SAME three functions below, the
// way `composure schema` and `stack/schema` share one `schema.Inspect`.
//
// THREE PROPERTIES THIS FILE EXISTS TO HOLD (DECISIONS.md 22):
//
//  1. It never returns an error for a network outcome. `hub.Look` answers with a
//     State and a sentence, and this prints it. `composure image lookup` on a
//     machine with no egress EXITS 0 — being offline is not a script failure and
//     a CI job that runs this must not go red because a runner has no route.
//  2. Every call is bounded and cancellable. `-timeout` is a real deadline on a
//     real context, defaulting to five seconds, because the thing on the other
//     end is a third party's undocumented endpoint.
//  3. `COMPOSURE_OFFLINE=1` refuses before the transport is entered, and the tests
//     assert on a request counter that it was never entered at all.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/hub"
)

// imageClient is the Docker Hub client, as a package variable so a test can
// point its three endpoints at a loopback server. Production never reassigns it.
var imageClient = hub.New()

// imageCache holds tag lists for the life of the process (R6.8). A `composure
// serve` session inspecting the same stack repeatedly asks Docker Hub once per
// repository per TTL rather than once per selection; the CLI, which exits, gets
// nothing from it and pays nothing for it.
var imageCache = hub.NewCache(clientLister{}, imageCacheTTL)

// imageCacheTTL is chosen against what the answer is used for: a reader deciding
// whether to bump a tag does not need a tag list fresher than the decision.
const imageCacheTTL = 5 * time.Minute

// imageTags is what every lookup goes through: the reader's switch OUTSIDE the
// cache, so `COMPOSURE_OFFLINE=1` suppresses a cached answer too.
//
// The guard was inside the cache first, and the test caught it: a repository
// looked up before the switch was thrown kept answering afterwards. The switch
// means "do not do this", not "do not open a socket" — a reader who turns the
// feature off and still sees upgrade pills has been told something false about
// their own settings.
func imageTags() hub.TagLister { return hub.Guarded(imageCache) }

// clientLister adapts the client to hub.TagLister and reads `imageClient` at
// call time, so a test's substitution is seen through the cache too.
type clientLister struct{}

func (clientLister) Tags(ctx context.Context, repo string, pageSize int, filter string) ([]hub.Tag, hub.RateLimit, error) {
	return imageClient.TagsContext(ctx, repo, pageSize, filter)
}

// defaultImageTimeout bounds one question. Long enough for a slow link, short
// enough that a reader who typed a name is not left wondering.
const defaultImageTimeout = 5 * time.Second

/* ---- the three answers, shared by the CLI and by `serve` ---------------- */

// lookupImage is `image/lookup` and `composure image lookup`, one function.
func lookupImage(ctx context.Context, ref string) hub.Report {
	return hub.Look(ctx, imageTags(), ref)
}

// imageSearchResult is what a search answers with. It carries the STATE and the
// SENTENCE for the same reason a lookup does: a popup that goes empty when
// Docker Hub is rate-limiting reads as "there is no such image".
type imageSearchResult struct {
	Query     string        `json:"query"`
	State     hub.State     `json:"state"`
	Message   string        `json:"message"`
	Results   []hub.Repo    `json:"results"`
	RateLimit hub.RateLimit `json:"rate_limit,omitzero"`
}

func searchImages(ctx context.Context, query string, limit int) imageSearchResult {
	out := imageSearchResult{Query: query, State: hub.StateOK, Results: []hub.Repo{}}
	if strings.TrimSpace(query) == "" {
		out.State = hub.StateNotComparable
		out.Message = "Type a name to search Docker Hub."
		return out
	}
	if hub.Off() {
		out.State = hub.StateDisabled
		out.Message = "Docker Hub search is switched off, so nothing here reaches the network. " +
			"You can still type any image reference you like."
		return out
	}
	repos, rl, err := imageClient.SearchContext(ctx, query, limit)
	out.RateLimit = rl
	if err != nil {
		// The same classification the lookup uses, from the same function,
		// because a reader who sees one sentence in the pane and a different
		// one in the popup for the same 429 learns nothing from either. The
		// subject is empty: a failed SEARCH is not about a repository, and
		// naming one would invent a repository the reader never asked for.
		out.State, out.Message = hub.Describe(err, "")
		return out
	}
	if len(repos) > 0 {
		out.Results = repos
	}
	if len(out.Results) == 0 {
		out.State = hub.StateNotFound
		out.Message = fmt.Sprintf("Docker Hub has nothing matching %q.", query)
	}
	return out
}

// imageStage is one build stage's base image and what is known about it.
type imageStage struct {
	Index int `json:"index"`
	/** The AS name, empty when the stage has none. */
	Name      string     `json:"name,omitempty"`
	Reference string     `json:"reference"`
	Report    hub.Report `json:"report"`
}

// imageStaleResult is R6.5 for a whole Dockerfile, and the source of the stage
// form's header fact.
type imageStaleResult struct {
	Path   string       `json:"path"`
	Stages []imageStage `json:"stages"`
	// OldestBase is the age of the OLDEST stage base in the file — "14 months
	// old" — because the header is a claim about the file and not about its
	// first stage. Empty when no stage has a comparable age.
	OldestBase     string `json:"oldest_base,omitempty"`
	OldestBaseDays int    `json:"oldest_base_days,omitempty"`
}

func staleImages(ctx context.Context, path string) (imageStaleResult, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return imageStaleResult{}, err
	}
	f := dockerfile.Parse(src)
	out := imageStaleResult{Path: path, Stages: []imageStage{}}
	for i, at := range f.Stages() {
		in := f.Instructions[at]
		r := lookupImage(ctx, in.ImageRef)
		out.Stages = append(out.Stages, imageStage{
			Index:     i,
			Name:      in.StageName,
			Reference: in.ImageRef,
			Report:    r,
		})
		if r.AgeDays > out.OldestBaseDays {
			out.OldestBaseDays, out.OldestBase = r.AgeDays, r.Age
		}
	}
	return out, nil
}

/* ---- the command ------------------------------------------------------- */

// runImage dispatches `composure image ...` and returns the process exit code.
//
// It returns a code rather than calling os.Exit so the tests can drive it in
// process, which is the only way to point the client at a loopback server
// without inventing an environment variable that would then exist in production
// for no other reason. `TestImageSubcommandIsReachableFromMain` runs the real
// binary so that deleting the dispatch is still a failing test.
func runImage(args []string) int {
	if len(args) == 0 {
		imageUsage()
		return 2
	}
	switch args[0] {
	case "lookup":
		fs := flag.NewFlagSet("image lookup", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		timeout := fs.Duration("timeout", defaultImageTimeout, "how long to wait for Docker Hub")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			fmt.Fprint(os.Stderr, "composure: image lookup needs one image reference, e.g. postgres:16-alpine\n")
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		printLookup(lookupImage(ctx, fs.Arg(0)), *asJSON)
		return 0

	case "search":
		fs := flag.NewFlagSet("image search", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		limit := fs.Int("limit", 15, "how many results to ask for")
		timeout := fs.Duration("timeout", defaultImageTimeout, "how long to wait for Docker Hub")
		_ = fs.Parse(args[1:])
		if fs.NArg() == 0 {
			fmt.Fprint(os.Stderr, "composure: image search needs a query\n")
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		printSearch(searchImages(ctx, strings.Join(fs.Args(), " "), *limit), *asJSON)
		return 0

	case "stale":
		fs := flag.NewFlagSet("image stale", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of the table")
		timeout := fs.Duration("timeout", defaultImageTimeout, "how long to wait for Docker Hub")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			fmt.Fprint(os.Stderr, "composure: image stale needs one path, the Dockerfile\n")
			return 2
		}
		// One deadline for the whole file rather than one per stage: the reader
		// asked one question and a ten-stage Dockerfile must not take ten times
		// as long to say "offline".
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		res, err := staleImages(ctx, fs.Arg(0))
		if err != nil {
			// A file that is not there IS a script failure — it is about the
			// reader's arguments rather than about the network.
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			return 1
		}
		printStale(res, *asJSON)
		return 0

	default:
		fmt.Fprintf(os.Stderr, "composure: unknown image command %q\n\n", args[0])
		imageUsage()
		return 2
	}
}

func imageUsage() {
	fmt.Fprintln(os.Stderr, `composure image — Docker Hub lookup and search

  composure image lookup [-json] [-timeout 5s] <reference>   what this image is, and what is newer
  composure image search [-json] [-limit 15] <query>         find an image by name
  composure image stale  [-json] [-timeout 5s] <Dockerfile>  every stage's base, and the file's age

A state that is not "ok" still exits 0: being offline is not a script failure.
COMPOSURE_OFFLINE=1 switches every request off before the transport is entered.`)
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func printLookup(r hub.Report, asJSON bool) {
	if asJSON {
		emitJSON(r)
		return
	}
	fmt.Printf("\nIMAGE — %s\n", r.Reference)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("state:  %s\n", r.State)
	fmt.Printf("        %s\n", wrap(r.Message, 68, "        "))
	if r.Age != "" {
		fmt.Printf("\ncurrent: %s", r.Age)
		if r.CurrentSize > 0 {
			fmt.Printf(", %s", hub.HumanBytes(r.CurrentSize))
		}
		fmt.Println()
	}
	if r.Candidate != nil {
		fmt.Printf("\nUPGRADE  %s\n", r.Pill)
	}
	if len(r.Alternatives) > 0 {
		fmt.Printf("\nALSO AVAILABLE (%d)\n", len(r.Alternatives))
		for _, a := range r.Alternatives {
			fmt.Printf("  %-32s %-6s %10s\n", a.Reference, a.Kind, hub.HumanBytes(a.Size))
		}
	}
	if r.RateLimit.Limit != "" {
		fmt.Printf("\nquota %s/%s remaining\n", r.RateLimit.Remaining, r.RateLimit.Limit)
	}
	fmt.Println()
}

func printSearch(res imageSearchResult, asJSON bool) {
	if asJSON {
		emitJSON(res)
		return
	}
	fmt.Printf("\nDOCKER HUB — %q\n", res.Query)
	fmt.Println(strings.Repeat("=", 78))
	if res.Message != "" {
		fmt.Printf("%s\n\n", wrap(res.Message, 76, ""))
	}
	if len(res.Results) == 0 {
		fmt.Println()
		return
	}
	fmt.Printf("%-34s %-18s %8s %8s  %s\n", "REPOSITORY", "BADGE", "PULLS", "STARS", "DESCRIPTION")
	for _, r := range res.Results {
		fmt.Printf("%-34s %-18s %8s %8d  %s\n",
			clip(r.Name, 34), clip(r.Badge, 18), r.PullsDisplay, r.Stars, clip(r.Description, 40))
	}
	if res.RateLimit.Limit != "" {
		fmt.Printf("\nquota %s/%s remaining\n", res.RateLimit.Remaining, res.RateLimit.Limit)
	}
	fmt.Println()
}

func printStale(res imageStaleResult, asJSON bool) {
	if asJSON {
		emitJSON(res)
		return
	}
	fmt.Printf("\nBASE IMAGES — %s\n", res.Path)
	fmt.Println(strings.Repeat("=", 78))
	if res.OldestBase != "" {
		fmt.Printf("%d stages · base image %s\n", len(res.Stages), res.OldestBase)
	} else {
		fmt.Printf("%d stages\n", len(res.Stages))
	}
	for _, st := range res.Stages {
		label := fmt.Sprintf("stage %d", st.Index)
		if st.Name != "" {
			label += " — " + st.Name
		}
		fmt.Printf("\n%s\n  FROM %s\n", label, st.Reference)
		if st.Report.Age != "" {
			fmt.Printf("  %s\n", st.Report.Age)
		}
		if st.Report.Candidate != nil {
			fmt.Printf("  UPGRADE  %s\n", st.Report.Pill)
		} else {
			fmt.Printf("  %s\n", wrap(st.Report.Message, 74, "  "))
		}
	}
	fmt.Println()
}

// wrap breaks a sentence at word boundaries so a paragraph of prose in a table
// does not run off the terminal. The sentences here are deliberately long,
// because a reader who has just been told "rate limited" needs to know it is
// not their fault and that it passes.
func wrap(s string, width int, indent string) string {
	if s == "" {
		return ""
	}
	var out strings.Builder
	line := 0
	for i, word := range strings.Fields(s) {
		switch {
		case i == 0:
			out.WriteString(word)
			line = len(word)
		case line+1+len(word) > width:
			out.WriteString("\n" + indent + word)
			line = len(word)
		default:
			out.WriteString(" " + word)
			line += 1 + len(word)
		}
	}
	return out.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
