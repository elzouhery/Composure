package main

// `composure add` — stories 7.3 and 7.4 at the headless door.
//
// It is `preview`/`apply` with a planner in front of it: the reader names a
// kind and a name, `edit.Plan` turns that into the operations the splice engine
// already performs, and the SAME `edit.Preview`/`edit.Apply` pair executes them
// as one atomic edit. No second write path, and no operation this command
// invented — a service is `insert_key` twice, which is why `-write` produces
// one diff and one undo rather than a service now and an image afterwards.
//
// The default is a preview, because the product's claim is that you see the
// diff before it lands. `-write` is the same boolean `apply` is.

import (
	"encoding/json"
	"os"

	"github.com/elzouhery/composure/internal/edit"
	"github.com/elzouhery/composure/internal/resolve"
)

type addFlags struct {
	kind  string
	name  string
	value string
}

// runAdd plans the declaration and previews or applies it. It never returns on
// failure: a refusal exits 3 with a stable slug, exactly as `apply` does.
func runAdd(path string, project *resolve.Project, f addFlags, write, asJSON bool) {
	ops, err := edit.Plan(edit.Add{
		File:   path,
		Kind:   f.kind,
		Name:   f.name,
		Value:  f.value,
		Merged: project,
	})
	if err != nil {
		reportEditFailure(path, err, asJSON)
	}

	req := edit.Request{File: path, Ops: ops}
	var res *edit.Result
	if write {
		res, err = edit.Apply(req)
	} else {
		res, err = edit.Preview(req)
	}
	if err != nil {
		reportEditFailure(path, err, asJSON)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			os.Exit(1)
		}
		return
	}
	printEdit(res)
}

// addProject resolves the project the declaration joins, for the duplicate-name
// check across a multi-file chain — best effort, on purpose.
//
// A project that does not resolve is not an error here. The stack a reader most
// needs to add a service to is the one with no services in it, and that stack
// does not resolve at all (`internal/resolve/load.go` validate). Refusing to
// add to it because of that would turn EXPERIENCE.md's starting point into the
// dead end this story exists to remove; the file's own duplicate check still
// runs, and it is the one that matters for the file being written.
func addProject(chain fileChain, path string) *resolve.Project {
	if len(chain) > 0 {
		project, err := resolve.Files(chain...)
		if err != nil {
			return nil
		}
		return project
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		project, err := resolve.Dir(path)
		if err != nil {
			return nil
		}
		return project
	}
	project, err := resolve.File(path)
	if err != nil {
		return nil
	}
	return project
}
