package dockerfile

import (
	"path/filepath"
	"strings"
	"testing"
)

const formSample = "# syntax=docker/dockerfile:1\n" +
	"# escape=\\\n" +
	"ARG NODE=18\n" +
	"\n" +
	"FROM --platform=$BUILDPLATFORM node:${NODE}-alpine AS build  # pinned\n" +
	"WORKDIR /src\n" +
	"# install first\n" +
	"RUN npm ci \\\n" +
	"    --omit=dev\n" +
	"\n" +
	"from nginx:1.27-alpine as runtime\n" +
	"COPY --from=build /src/dist /usr/share/nginx/html\n" +
	"EXPOSE 80\n"

// R5.6: one group per build stage, instructions in order. No graph, because a
// Dockerfile is a linear list and a graph adds nothing.
func TestBuildFormGroupsByStageInOrder(t *testing.T) {
	f := BuildForm("Dockerfile", []byte(formSample))
	if len(f.Stages) != 2 {
		t.Fatalf("%d stages, want 2", len(f.Stages))
	}
	if f.Stages[0].Name != "build" || f.Stages[0].Label != "build" {
		t.Errorf("stage 0 is %+v, want the AS name", f.Stages[0])
	}
	if f.Stages[1].ImageRef != "nginx:1.27-alpine" || f.Stages[1].Name != "runtime" {
		t.Errorf("stage 1 is %+v", f.Stages[1])
	}
	// Instruction order is the file's order and is never sorted: a Dockerfile's
	// semantics are positional, and reordering the form would teach the reader
	// something untrue about their build.
	var names []string
	for _, in := range f.Stages[0].Instructions {
		if in.Kind == "instruction" {
			names = append(names, in.Name)
		}
	}
	if strings.Join(names, ",") != "WORKDIR,RUN" {
		t.Errorf("stage 0 instructions are %v, want WORKDIR then RUN", names)
	}
	// The ARG before the first FROM decides the base image; dropping it would
	// hide the declaration the reader most needs to see.
	if len(f.Preamble) == 0 || f.Preamble[0].Name != "ARG" {
		t.Errorf("the pre-FROM ARG is not in the preamble: %+v", f.Preamble)
	}
	if len(f.Directives) != 2 {
		t.Errorf("%d directives, want the syntax and escape lines", len(f.Directives))
	}
}

// R7.4 stated in the form rather than discovered at save time. A field the
// engine will refuse to write must not be offered as editable.
func TestBuildFormMarksAMultiLineInstructionNotEditable(t *testing.T) {
	f := BuildForm("Dockerfile", []byte(formSample))
	var run InstructionView
	for _, in := range f.Stages[0].Instructions {
		if in.Name == "RUN" {
			run = in
		}
	}
	if run.Name == "" {
		t.Fatal("no RUN in stage 0")
	}
	if run.Editable {
		t.Error("a two-line RUN is offered as editable; saving it would be refused")
	}
	if run.NotEditable == "" {
		t.Error("nothing says why it cannot be edited")
	}
	if run.StartLine == run.EndLine {
		t.Errorf("the continuation was not folded into one instruction: %d-%d", run.StartLine, run.EndLine)
	}
	if !strings.Contains(run.Text, "--omit=dev") {
		t.Errorf("the instruction text does not carry its continuation: %q", run.Text)
	}
}

// The byte range the form reports for a base image is the range SetBaseImage
// overwrites — the reference alone. Two answers to "where is the image" is the
// divergence AD-14 forbids.
func TestFormImageRangeIsTheRangeSetBaseImageWrites(t *testing.T) {
	src := []byte(formSample)
	f := BuildForm("Dockerfile", src)
	from := f.Stages[1].From
	if got := string(src[from.ImageStart:from.ImageEnd]); got != "nginx:1.27-alpine" {
		t.Fatalf("the reported range holds %q", got)
	}
	out, err := Parse(src).SetBaseImage(1, "nginx:1.28-alpine")
	if err != nil {
		t.Fatal(err)
	}
	want := string(src[:from.ImageStart]) + "nginx:1.28-alpine" + string(src[from.ImageEnd:])
	if string(out) != want {
		t.Error("SetBaseImage touched bytes the form did not report")
	}
}

func TestBuildFormReportsEndingsAndMark(t *testing.T) {
	f := BuildForm("Dockerfile", []byte("\ufeffFROM alpine\r\nRUN true\r\n"))
	if !f.BOM {
		t.Error("the BOM was not reported")
	}
	if !f.CRLF {
		t.Error("CRLF was not reported")
	}
	if len(f.Stages) != 1 {
		t.Errorf("%d stages, want 1 — a BOM must not hide the first FROM", len(f.Stages))
	}
}

func TestBuildFormReportsACustomEscapeCharacter(t *testing.T) {
	f := BuildForm("Dockerfile", []byte("# escape=`\nFROM alpine\n"))
	if f.EscapeChar != "`" {
		t.Errorf("escape char is %q, want a backtick", f.EscapeChar)
	}
}

func TestMissingFormNamesTheFileThatIsNotThere(t *testing.T) {
	f := MissingForm("/p/api/Dockerfile.dev", "./api", "Dockerfile.dev")
	if !f.Missing || f.Path != "/p/api/Dockerfile.dev" {
		t.Errorf("%+v", f)
	}
	if len(f.Stages) != 0 {
		t.Error("a missing file has stages")
	}
}

// ResolveBuild is Compose's own reading: the dockerfile is relative to the
// context and the context is relative to the compose file. It is the one
// implementation the diagnostic and the RPC share, so a file reported missing
// is the file the editor would have opened.
func TestResolveBuild(t *testing.T) {
	base := filepath.FromSlash("/p")
	cases := []struct {
		context, name, want string
		ok                  bool
	}{
		{"", "", filepath.FromSlash("/p/Dockerfile"), true},
		{".", "", filepath.FromSlash("/p/Dockerfile"), true},
		{"./api", "", filepath.FromSlash("/p/api/Dockerfile"), true},
		{"./api", "Dockerfile.dev", filepath.FromSlash("/p/api/Dockerfile.dev"), true},
		{"api/", "build/Dockerfile", filepath.FromSlash("/p/api/build/Dockerfile"), true},
		{"", filepath.FromSlash("/abs/Dockerfile"), filepath.FromSlash("/abs/Dockerfile"), true},
		// Nothing local to check, so nothing is claimed.
		{"https://github.com/x/y.git", "", "", false},
		{"git@github.com:x/y.git", "", "", false},
		{"${BUILD_CONTEXT}/api", "", "", false},
		{"./api", "${DOCKERFILE}", "", false},
	}
	for _, c := range cases {
		got, ok := ResolveBuild(base, c.context, c.name)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ResolveBuild(%q, %q, %q) = %q, %v; want %q, %v",
				base, c.context, c.name, got, ok, c.want, c.ok)
		}
	}
}
