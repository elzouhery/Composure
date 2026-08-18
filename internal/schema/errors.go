package schema

import "errors"

// ErrSchemaUnreadable is the vendored specification failing to parse, or
// parsing into something that is not a schema.
//
// It is a refusal, not a degradation. A schema that loaded as an empty
// document would produce an empty `available, not set` list on every node —
// the inspector's whole differentiator switched off, silently, while every
// other part of the pane kept working. CLAUDE.md rule 6: say no loudly.
var ErrSchemaUnreadable = errors.New("schema: the vendored Compose specification could not be read")

// ErrNoProject is Inspect called with nothing to inspect.
var ErrNoProject = errors.New("schema: no resolved project")

// ErrUnknownPath is a config path that the resolved model does not contain.
// Distinct from a path the SCHEMA does not describe, which is not an error at
// all — see Spec.Child.
var ErrUnknownPath = errors.New("schema: no such path in this project")
