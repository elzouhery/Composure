// Command hubsearch demonstrates image discovery joined to the edit engine.
//
//	hubsearch search <query>        search Docker Hub
//	hubsearch tags   <repo>         list tags with size, age and architectures
//	hubsearch stale  <Dockerfile>   read the FROM, find newer tags, print the diff
//
// The `stale` subcommand is the point. Search on its own is a feature anyone can
// build against a public API. Search that ends in a one-line, comment-preserving
// diff to your actual Dockerfile is the product — and it only works because the
// splice engine is underneath it.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/hub"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	c := hub.New()
	switch os.Args[1] {
	case "search":
		search(c, strings.Join(os.Args[2:], " "))
	case "tags":
		tags(c, os.Args[2])
	case "stale":
		stale(c, os.Args[2])
	default:
		usage()
	}
}

func search(c *hub.Client, q string) {
	repos, rl, err := c.Search(q, 15)
	if err != nil {
		die(err)
	}
	fmt.Printf("\nDocker Hub — %q   (quota %s/%s remaining)\n\n", q, rl.Remaining, rl.Limit)
	fmt.Printf("%-34s %-18s %8s %8s  %s\n", "REPOSITORY", "BADGE", "PULLS", "STARS", "DESCRIPTION")
	for _, r := range repos {
		fmt.Printf("%-34s %-18s %8s %8d  %s\n",
			truncate(r.Name, 34), truncate(r.Badge, 18), r.PullsDisplay, r.Stars,
			truncate(r.Description, 40))
	}
	fmt.Println()
}

func tags(c *hub.Client, repo string) {
	ts, rl, err := c.Tags(repo, 20, "")
	if err != nil {
		die(err)
	}
	fmt.Printf("\n%s — tags   (quota %s/%s remaining)\n\n", hub.NormalizeRepo(repo), rl.Remaining, rl.Limit)
	fmt.Printf("%-28s %10s %12s  %s\n", "TAG", "SIZE", "AGE", "ARCHITECTURES")
	for _, t := range ts {
		fmt.Printf("%-28s %10s %12s  %s\n",
			truncate(t.Name, 28), humanBytes(t.FullSize), age(t.LastPushed),
			truncate(strings.Join(t.Architectures(), " "), 46))
	}
	fmt.Println()
}

// stale is the end-to-end demonstration: parse a real Dockerfile, look up its
// base image, and produce the exact edit that would update it.
//
// The candidate SELECTION is no longer here. It moved to `internal/hub`
// (`hub.Look`) when Epic 8 gave the capability a CLI, an RPC method and a pane:
// this file is a `main` package, so the rules that decide what may be offered as
// an upgrade could not be called by any of the three, and each of them would
// have grown its own answer to "is `alpine:edge` an upgrade". One copy, four
// callers.
func stale(c *hub.Client, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		die(err)
	}
	f := dockerfile.Parse(src)
	stages := f.Stages()
	if len(stages) == 0 {
		die(fmt.Errorf("no FROM instruction found in %s", path))
	}

	lister := hub.Guarded(hub.NewCache(clientTags{c}, 5*time.Minute))
	for si := range stages {
		in := f.Instructions[stages[si]]
		fmt.Printf("\nstage %d — %s", si, in.ImageRef)
		if in.StageName != "" {
			fmt.Printf("  (AS %s)", in.StageName)
		}
		fmt.Println()

		r := hub.Look(context.Background(), lister, in.ImageRef)
		if r.Age != "" {
			fmt.Printf("  current tag is %s, %s\n", r.Age, humanBytes(r.CurrentSize))
		}
		if r.Candidate == nil {
			fmt.Printf("  %s\n", r.Message)
			continue
		}
		fmt.Printf("  candidate: %s\n", r.Pill)

		out, err := f.SetBaseImage(si, r.Candidate.Reference)
		if err != nil {
			fmt.Printf("  edit failed: %v\n", err)
			continue
		}
		fmt.Println("  diff:")
		printDiff(string(src), string(out))
	}
	fmt.Println()
}

// clientTags adapts the client to hub.TagLister.
type clientTags struct{ c *hub.Client }

func (a clientTags) Tags(ctx context.Context, repo string, pageSize int, filter string) ([]hub.Tag, hub.RateLimit, error) {
	return a.c.TagsContext(ctx, repo, pageSize, filter)
}

func printDiff(a, b string) {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range al {
		if i < len(bl) && al[i] != bl[i] {
			fmt.Printf("    - %s\n    + %s\n", al[i], bl[i])
		}
	}
}

func age(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for n/div >= u && exp < 3 {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func usage() {
	fmt.Fprintln(os.Stderr, `hubsearch — Docker Hub discovery wired to the edit engine

  hubsearch search <query>       search Docker Hub
  hubsearch tags   <repo>        list tags with size, age, architectures
  hubsearch stale  <Dockerfile>  find newer base images and print the exact diff`)
	os.Exit(2)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
