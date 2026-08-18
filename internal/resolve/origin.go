package resolve

import "fmt"

// Origin says where a value came from: which file, which line, which column,
// and which position that file holds in the project's merge order.
//
// This is the requirement the whole product is built to answer — "why is this
// service getting that port" — and it is the one thing that cannot be
// retrofitted. A merge built without provenance gets rewritten rather than
// extended, so every value carries an Origin from the first commit, including
// in the single-file case where nothing can have been overridden.
type Origin struct {
	// File is the path the value was read from, as given to the resolver.
	File string `json:"file"`
	// Line and Column are 1-based and address the VALUE token, not its key.
	//
	// The distinction matters downstream: the splice engine locates keys
	// because it replaces whole subtrees, but an inspector highlights what a
	// user actually sees, which is the value. The key position is always
	// recoverable from the path.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Step is the index of File in the project's ordered source-file list,
	// after include and extends expansion. It is 0 while only one file exists.
	//
	// Indexing the file list rather than counting merge operations is what
	// makes a step number meaningful on its own: "step 2" names a file every
	// consumer can look up, not a position in a walk nobody else can replay.
	Step int `json:"step"`
}

// IsZero reports whether an Origin fails to address a real position.
//
// The check requires a line and column, not merely a file. An earlier version
// tested `File == "" && Line == 0 && Column == 0`, which returned false for a
// positionless origin carrying only a filename — so values with no position at
// all counted toward the "every leaf carries provenance" metric and the number
// proved nothing.
func (o Origin) IsZero() bool { return o.File == "" || o.Line < 1 || o.Column < 1 }

func (o Origin) String() string { return fmt.Sprintf("%s:%d:%d", o.File, o.Line, o.Column) }

// Override records a value that was replaced during merge, and where it came
// from. The list is present and empty in the single-file case — a field, not
// an absence — so that consumers written now do not need rewriting when
// merging arrives, and so "never overridden" is expressible rather than
// indistinguishable from "not yet implemented".
type Override struct {
	// Value is the replaced scalar as written. Sequences and mappings record
	// the origin only; reconstructing a replaced subtree is the file's job.
	Value  string `json:"value"`
	Origin Origin `json:"origin"`
}

// SourceFile is one entry in a project's ordered file list. Step is the index
// Origin.Step refers to.
type SourceFile struct {
	Path string `json:"path"`
	Step int    `json:"step"`
}
