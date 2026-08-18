package main

// `composure serve` is the long-lived surface the VS Code extension talks to: a
// JSON-RPC 2.0 server over stdio with LSP-style `Content-Length` framing, the
// arrangement gopls and rust-analyzer use.
//
// It is a transport and never a second implementation. `stack/resolve` calls
// resolve.File and marshals the *resolve.Project with the same marshaller
// `composure resolve -json` uses, so the two surfaces cannot drift.
//
// Framing rather than newline-delimited JSON because compose values contain
// newlines and resolved payloads are large; `Content-Length` degrades safely on
// a partial write — the reader blocks rather than parsing half a message as a
// whole one.
//
// stdout carries the protocol and nothing else. Every diagnostic goes to
// stderr; a stray Println on stdout corrupts the frame stream.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elzouhery/composure/internal/diagnose"
	"github.com/elzouhery/composure/internal/edit"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/schema"
	"github.com/elzouhery/composure/internal/topology"
)

// protocolRevision is bumped when the wire shape changes incompatibly. The
// client compares it at handshake and refuses a core it cannot speak to, which
// is cheaper than debugging a field that silently stopped arriving.
// Revision 2 added the write path — stack/preview, stack/apply — and
// stack/dockerfile. A client that has a Save button and a core that has no
// apply method is a skew the handshake has to catch, because the alternative is
// a method-not-found the reader sees only after pressing the one control in the
// product that touches their file.
// Revision 3 added stack/impact, which the canvas's focus mode calls. Same
// reasoning: a client with a focus control and a core with no impact method
// fails at the gesture rather than at the handshake, and the reader is the one
// who finds out.
// Revision 4 added stack/add — stories 7.3 and 7.4. Same reasoning a third
// time: a client whose stack pane offers `+ add service` and a core with no
// plan method fails at the press, and the reader finds out with a name typed
// into a form that then goes nowhere.
// Revision 5 added stack/editable — decision 21. It is a bump rather than an
// additive change for the reason the four before it were: a client that stages
// an override for an inherited value, talking to a core that cannot say which
// values are inherited, stages a `replace_scalar` against a path with no bytes
// and the reader is told their edit "could not be made". That is the exact
// defect this method closes, reappearing as a version skew, and the handshake
// is where it costs nothing.
// Revision 6 added image/lookup and image/search — Epic 8, DECISIONS.md 22.
// Same reasoning a sixth time, and it bites harder here than anywhere before
// it: this is the first method whose answer the client CANNOT compute for
// itself, so a client with a search popup and a core with no method leaves the
// reader typing into a box that answers nothing. It is also the first method
// that reaches the network, which is why the handshake is where the skew has to
// be caught rather than the first keystroke.
// Revision 7 is stack/extract and stack/extract-apply — story 9.3. The
// revision was deliberately NOT bumped when those methods landed, because a
// revision exists so that a client with a button and a core without the method
// fails at the handshake rather than in front of the reader, and no client had
// the button. Epic 9's extension half is that client, and this is the bump it
// was held for. It bites in a new way: this is the only method in the protocol
// that writes TWO files, so a client that offers `Move to a variable` against a
// core that cannot perform it would leave the reader having approved two diffs
// and produced neither.
// Revision 8 is the first bump that is NOT a new method. `diagnose.Fix` gained
// a `remedy` field — story 9.5, DECISIONS.md 26 — and a field is a wire shape
// exactly as a method is. It is bumped rather than treated as additive for one
// reason: a client that renders `and: composure extract …` against a core that
// emits no remedy shows the reader an empty affordance, and an editor that
// offers a fix it cannot describe is the confident wrong answer in the surface
// whose whole job is to be trusted. Failing at the handshake costs a version
// message; failing at the gesture costs the pane.
//
// `stack/extract-arg` and `stack/extract-arg-apply` — story 9.4 — would NOT
// have justified a bump on their own: revision 7's note records the rule, which
// is that a method with no client is not a bump.
//
// Revision 9 is story 9.6, and it is the two-file write joining the staleness
// contract. `stack/extract` and `stack/extract-apply` gained `expect` and
// `expect_env` on the request, and the response gained `env_expect` — the
// `.env` half's assertion, which is about the VARIABLE rather than a byte
// range, because the `.env` edit is one appended line and has no range to
// assert. Every one of those fields is OPTIONAL, so the wire is additive; the
// bump is not about parsing.
//
// It is a bump because of what a client that does not send them GETS: the one
// write path in this product with a blast radius larger than the file the
// reader is looking at, with no staleness protection at all, while every other
// staged write in the product has had it since AD-19. A client that shows
// "this stage is stale, discard it" for a scalar edit and silently overwrites a
// colleague's `.env` for a move is exactly the skew the handshake exists to put
// in front of us rather than in front of the reader. Story 9.4's two methods
// ride along in the same revision.
const protocolRevision = 9

// serverVersion identifies the core build. It is not the protocol revision:
// the binary can move without the wire moving.
const serverVersion = "0.2.0"

// JSON-RPC error codes. The negative 32000 block is reserved for
// implementation-defined server errors by the spec.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603

	// codeResolveFailed is a compose file the core refused. It is not a
	// protocol fault — the request was well formed — so it gets its own code
	// and carries {file, line, column} in data.
	codeResolveFailed = -32001

	// codeEditRefused is an edit the engine declined to perform because
	// performing it would damage the file: a flow-style collection that cannot
	// take a block child, a multi-line instruction that would need reflowing.
	// The client reverts the field and says what could not be done. Nothing was
	// written.
	codeEditRefused = -32002

	// codeStaleRange is an edit staged against a byte range that has since
	// moved (AD-19). It is separate from a refusal because the client's
	// response differs: a refusal is worked around, a stale stage is DISCARDED.
	codeStaleRange = -32003

	// codeEditFailed is an edit that could not be carried out for an ordinary
	// reason — a path that does not resolve, a file that could not be read.
	codeEditFailed = -32004

	// codeNoSuchPath is a config path that addresses nothing in the resolved
	// model (story 1.10). It carries the closest paths that do exist, because
	// "no such path" alone sends the reader hunting for a typo they have
	// already read four times.
	codeNoSuchPath = -32005
)

// maxFrameBytes caps a single message body. A client that announces a
// gigabyte-long frame is either broken or hostile, and io.ReadFull would
// happily allocate it first and fail second.
const maxFrameBytes = 64 << 20

// maxHeaderLines caps the header block of one frame, so a stream of endless
// header lines cannot spin forever without ever producing a message.
const maxHeaderLines = 64

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports whether the request carries no id and therefore must
// not be answered. JSON `null` is not an id either.
func (r *rpcRequest) isNotification() bool {
	s := strings.TrimSpace(string(r.ID))
	return s == "" || s == "null"
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// rpcResponse carries a pre-encoded result so that a null result is still a
// present `"result": null` field. JSON-RPC requires exactly one of result and
// error; an `omitempty` on an `any` would silently drop `shutdown`'s null
// result and emit a response that is neither.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// initializeResult is the handshake. The client learns what it is speaking to
// before it sends anything that matters.
type initializeResult struct {
	ServerName string `json:"serverName"`
	Version    string `json:"version"`
	Protocol   int    `json:"protocol"`
}

type resolveParams struct {
	Path string `json:"path"`
	// Files is an explicit `-f` chain (story 1.6), merged left to right. When
	// it is present, Path is used only for the report header and the automatic
	// compose.override.yaml pickup is disabled, exactly as on the command line.
	Files []string `json:"files"`
}

// topologyParams is stack/resolve's params plus the active profile set. It is
// its own type rather than an extension of resolveParams so that a client
// sending profiles to stack/resolve gets them ignored loudly by the schema
// rather than quietly by the server.
type topologyParams struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
	// Profiles is optional. Absent means Compose's default — only
	// always-active services — which is the same thing an empty array means.
	Profiles []string `json:"profiles"`
}

// schemaParams is stack/schema's request. `at` is a config path — the same
// join key everything else uses — and empty means the stack itself.
type schemaParams struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
	At    string   `json:"at"`
	All   bool     `json:"all"`
}

// impactParams is stack/impact's request: a project, the profile set the canvas
// is drawing, and the config path being asked about. `at` is required — "the
// blast radius of the whole stack" is the graph itself, which stack/topology
// already answers.
type impactParams struct {
	Path     string   `json:"path"`
	Files    []string `json:"files"`
	Profiles []string `json:"profiles"`
	At       string   `json:"at"`
}

// explainParams is stack/explain's request — story 1.10. `at` is the config
// path being asked about; it is required, because "explain the whole project"
// is what stack/resolve already answers.
type explainParams struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
	At    string   `json:"at"`
}

// errorData is the payload of a failed stack/resolve. Line and column are
// omitted when the error has no position — a missing file has none — so a
// client can distinguish "no position" from "line 0".
type errorData struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// errFramingBroken means the byte stream can no longer be resynchronised. A
// frame boundary is not recoverable: once a Content-Length is wrong, every
// following byte is of unknown provenance, so the server stops rather than
// guessing.
var errFramingBroken = errors.New("framing is broken")

// server is the whole RPC surface. It holds no project state: a request names
// a path and gets a project back. A cache here is where a second, divergent
// model grows.
type server struct {
	in  *bufio.Reader
	out io.Writer
	log io.Writer // stderr; never stdout

	// ctx is the session's lifetime, handed to every handler that can block on
	// something other than this machine. Cancelling it is how a shutdown makes
	// an outstanding Docker Hub call return in milliseconds rather than at its
	// own five-second deadline.
	ctx context.Context

	// writeMu makes stdout a single-writer stream. A frame is a header write
	// followed by a body write, and two goroutines interleaving those two pairs
	// produces a `Content-Length` that describes somebody else's bytes — which
	// the client cannot resynchronise from, so it kills the core. This is the
	// one lock that is not about correctness of an answer but about the
	// existence of one.
	writeMu sync.Mutex

	// fs is the file plane. READS TAKE IT SHARED, WRITES TAKE IT EXCLUSIVE.
	//
	// Two `stack/apply` calls overlapping, or an apply writing while a preview
	// reads the same file, is a worse defect than the serialisation this
	// concurrency exists to remove: `edit.Apply` reads the file, splices the
	// buffer and writes it back, so an overlapping writer's bytes are computed
	// from a document that no longer exists, and an overlapping reader can see
	// a half-written one. It is deliberately ONE lock for all files rather than
	// one per file: every operation it guards is disk-local and bounded in
	// milliseconds, so the contention costs nothing measurable, while a
	// per-file lock would need a canonical path — and two paths naming one file
	// through a symlink would silently defeat it.
	//
	// The lookup — the only unbounded call in the process — holds NO lock at
	// all, which is the whole point.
	fs sync.RWMutex

	// inflight counts handlers running off the read loop, so a session that is
	// ending can wait for their answers instead of losing them.
	inflight sync.WaitGroup

	// shutdownRequested is touched only by the read loop, which is why
	// `shutdown` and `exit` are handled inline rather than on a goroutine.
	shutdownRequested bool
}

// drainGrace bounds how long a session that is ending waits for requests that
// are still outstanding. A variable so a test can shorten it; production never
// reassigns it.
//
// The wait exists so a response is not lost — the client's `dispose()` sends
// `shutdown` and `exit` back to back without waiting for anything, and a lookup
// in flight at that moment still has a caller. The bound exists because a core
// that will not exit is worse than one that answers late: it outlives the window
// that spawned it.
var drainGrace = 5 * time.Second

// serve runs the read/dispatch/write loop until the stream ends or `exit`
// arrives. It returns the process exit code.
//
// The LSP convention is kept: `exit` after `shutdown` is a clean stop, `exit`
// without one — or an input stream that simply ends — is not, because it means
// the client vanished mid-session and a supervisor should be able to tell.
//
// The loop READS while handlers run. That is not an optimisation: stdin is a
// pipe, so a server inside a handler is not reading either, and a client whose
// second request cannot even be delivered is worse off than one whose answer
// arrives late (DECISIONS.md 22 — the pane must never be blocked by a lookup).
func serve(in io.Reader, out, logw io.Writer) int {
	ctx, cancel := context.WithCancel(context.Background())
	s := &server{in: bufio.NewReader(in), out: out, log: logw, ctx: ctx}
	code := s.loop()
	// Cancel first, then wait: an outstanding lookup returns `cancelled` — a
	// state with its own sentence — and writes its response before we go.
	cancel()
	s.drain()
	return code
}

// drain waits, once and boundedly, for the handlers still running.
func (s *server) drain() {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainGrace):
		fmt.Fprintf(s.log, "composure serve: gave up waiting %s for outstanding requests\n", drainGrace)
	}
}

func (s *server) loop() int {
	for {
		req, err := s.readFrame()
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				// The client closed the pipe. A shutdown first makes that
				// intentional; without one the client died.
				if s.shutdownRequested {
					return 0
				}
				fmt.Fprintln(s.log, "composure serve: input closed before shutdown")
				return 1
			case errors.Is(err, errFramingBroken):
				fmt.Fprintf(s.log, "composure serve: %v\n", err)
				// Answer with a null id so a client waiting on a response sees
				// a fault rather than a hang, then stop: the stream position is
				// no longer known.
				s.write(&rpcResponse{
					JSONRPC: "2.0",
					ID:      json.RawMessage("null"),
					Error:   &rpcError{Code: codeParse, Message: err.Error()},
				})
				return 1
			default:
				fmt.Fprintf(s.log, "composure serve: read: %v\n", err)
				return 1
			}
		}
		if req == nil {
			// A frame whose body was not JSON. The framing is still intact, so
			// the next frame is readable and the loop continues.
			continue
		}
		if stop := s.dispatch(req); stop {
			if s.shutdownRequested {
				return 0
			}
			return 1
		}
	}
}

// readFrame reads one `Content-Length`-framed message.
//
// It returns (nil, nil) when the body was not valid JSON: that is a client
// fault the stream survives, an error response has already been written, and
// the loop should read the next frame. Header faults return errFramingBroken
// because the stream cannot be resynchronised after one.
func (s *server) readFrame() (*rpcRequest, error) {
	length := -1
	for i := 0; ; i++ {
		if i >= maxHeaderLines {
			return nil, fmt.Errorf("%w: more than %d header lines", errFramingBroken, maxHeaderLines)
		}
		line, err := s.in.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if strings.TrimSpace(line) == "" && length < 0 && i == 0 {
					// Clean end of stream between frames.
					return nil, io.EOF
				}
				// A header block that stopped mid-way. Half a frame is not a
				// frame.
				return nil, fmt.Errorf("%w: stream ended inside a header block", errFramingBroken)
			}
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("%w: malformed header line %q", errFramingBroken, line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%w: bad Content-Length %q", errFramingBroken, strings.TrimSpace(value))
			}
			length = n
		}
		// Content-Type and any other header is accepted and ignored; the
		// charset is always UTF-8 here.
	}
	if length < 0 {
		return nil, fmt.Errorf("%w: frame has no Content-Length header", errFramingBroken)
	}
	if length > maxFrameBytes {
		return nil, fmt.Errorf("%w: frame of %d bytes exceeds the %d byte limit", errFramingBroken, length, maxFrameBytes)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(s.in, body); err != nil {
		// Short body: the announced length and the bytes disagree, so the next
		// byte is not a frame boundary.
		return nil, fmt.Errorf("%w: body of %d bytes was truncated: %v", errFramingBroken, length, err)
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.write(&rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: codeParse, Message: "request body is not valid JSON"},
		})
		return nil, nil
	}
	return &req, nil
}

// dispatch handles one request, converting a panic below it into an internal
// JSON-RPC error. It reports whether the loop should stop.
//
// The engine's characteristic failure is a confident wrong answer rather than a
// crash, but one adversarial file that does panic must not end a session that
// is drawing forty others: the client would see the process vanish and every
// in-flight call reject, for a fault local to a single request. The panic is
// reported on stderr — where the stack is useful — and answered on the wire, so
// the client learns which request failed instead of inferring it from a corpse.
//
// stdout stays protocol-only: the recovery writes a frame, never prose.
//
// This one guards the READ LOOP — the control plane and the classification. A
// handler that runs on its own goroutine is beyond its reach and defers the
// same recovery for itself; a recover only ever sees its own stack, and the
// alternative there is not a failed request but a dead process.
func (s *server) dispatch(req *rpcRequest) (stop bool) {
	// stop stays false through a recovery: a panic is local to a request, and
	// ending the session is the outcome this guard exists to prevent.
	defer s.recoverRequest(req)
	return s.dispatchRequest(req)
}

// plane is how a method may run.
//
// It is a property of the METHOD, decided in one table below, rather than
// something each handler negotiates for itself. A handler that took its own
// lock would be a second answer to "may this overlap a write", and the two
// would disagree the first time a method changed what it touches.
type plane int

const (
	// planeControl runs INLINE on the read loop: `initialize`, `shutdown`,
	// `exit`, and every envelope fault. They are pure, instant, and — for the
	// last two — ordered with respect to the stream itself. Handing `shutdown`
	// to a goroutine would race its own reply against the `exit` that follows
	// it on the wire.
	planeControl plane = iota

	// planeRead reads files and writes none. Concurrent with other reads and
	// with the network; excluded from the write path.
	planeRead

	// planeWrite WRITES FILES — `stack/apply`, `stack/extract-apply`. Exclusive
	// of every other file method, reads included.
	planeWrite

	// planeNetwork is the Docker Hub pair. It touches no file and takes no
	// lock, so nothing waits behind it. This is the plane the whole change
	// exists for.
	planeNetwork
)

// planeOf classifies a method. The second return is false for a method that
// does not exist, which is a control-plane failure.
//
// Read this table as the answer to "what can overlap what". Everything that
// reads a project is a read; the two methods that write bytes to a file are
// writes; the two that leave the machine are network.
func planeOf(method string) (plane, bool) {
	switch method {
	case "stack/resolve", "stack/explain", "stack/topology", "stack/impact",
		"stack/diagnose", "stack/schema", "stack/dockerfile", "stack/preview",
		"stack/add", "stack/editable", "stack/extract", "stack/extract-arg":
		return planeRead, true
	case "stack/apply", "stack/extract-apply", "stack/extract-arg-apply":
		return planeWrite, true
	case "image/lookup", "image/search":
		return planeNetwork, true
	}
	return planeControl, false
}

func (s *server) dispatchRequest(req *rpcRequest) (stop bool) {
	if req.JSONRPC != "2.0" {
		s.fail(req, codeInvalidRequest, `"jsonrpc" must be "2.0"`, nil)
		return false
	}
	if req.Method == "" {
		s.fail(req, codeInvalidRequest, `request has no "method"`, nil)
		return false
	}

	switch req.Method {
	case "initialize":
		s.reply(req, initializeResult{
			ServerName: "composure",
			Version:    serverVersion,
			Protocol:   protocolRevision,
		})
		return false
	case "shutdown":
		s.shutdownRequested = true
		// A null result, not an omitted one: the client is waiting for a
		// response before it sends `exit`.
		s.reply(req, nil)
		return false
	case "exit":
		return true
	}

	p, ok := planeOf(req.Method)
	if !ok {
		s.fail(req, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method), nil)
		return false
	}

	s.inflight.Add(1)
	go func() {
		defer s.inflight.Done()
		// This handler's OWN recover. `dispatch`'s guards the read loop and
		// cannot see a panic on another stack: an unrecovered one here takes
		// the whole process down, which is precisely the failure that guard
		// exists to prevent — now with forty other requests in flight to lose
		// with it. The unlocks are deferred after it and therefore run first,
		// so a panicking write does not leave the file plane locked forever.
		defer s.recoverRequest(req)
		switch p {
		case planeRead:
			s.fs.RLock()
			defer s.fs.RUnlock()
		case planeWrite:
			s.fs.Lock()
			defer s.fs.Unlock()
		}
		s.handle(req)
	}()
	return false
}

// recoverRequest is dispatch's recovery, on a handler goroutine.
func (s *server) recoverRequest(req *rpcRequest) {
	r := recover()
	if r == nil {
		return
	}
	fmt.Fprintf(s.log, "composure serve: panic handling %q: %v\n%s\n", req.Method, r, debug.Stack())
	s.fail(req, codeInternal, fmt.Sprintf("the core panicked handling %q", req.Method), nil)
}

// handle runs one method. It is only ever reached from a handler goroutine,
// under whatever lock its plane calls for.
func (s *server) handle(req *rpcRequest) {
	switch req.Method {
	case "stack/resolve":
		s.resolveStack(req)
	case "stack/explain":
		s.explainStack(req)
	case "stack/topology":
		s.topologyStack(req)
	case "stack/impact":
		s.impactStack(req)
	case "stack/diagnose":
		s.diagnoseStack(req)
	case "stack/schema":
		s.schemaStack(req)
	case "stack/dockerfile":
		s.dockerfileStack(req)
	case "stack/preview":
		s.editStack(req, false)
	case "stack/apply":
		s.editStack(req, true)
	case "stack/add":
		s.addToStack(req)
	case "stack/editable":
		s.editableStack(req)
	case "stack/extract", "stack/extract-apply":
		s.extractFromStack(req, req.Method == "stack/extract-apply")
	case "stack/extract-arg", "stack/extract-arg-apply":
		s.extractArgFromStack(req, req.Method == "stack/extract-arg-apply")
	case "image/lookup":
		s.lookupImage(req)
	case "image/search":
		s.searchImages(req)
	default:
		// Unreachable: planeOf and this switch are the same list, and a method
		// missing from either is a method-not-found on the control plane. It is
		// answered rather than dropped, because a client waiting on a response
		// that will never come is the one failure mode this transport must not
		// have.
		s.fail(req, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method), nil)
	}
}

// loadProject applies the same project-location rules the CLI does, so the
// editor and the command line cannot resolve two different projects from the
// same arguments: an explicit chain wins, a directory gets the candidate order
// plus its override file, and a named file is that file alone.
func loadProject(path string, files []string) (*resolve.Project, error) {
	if len(files) > 0 {
		return resolve.Files(files...)
	}
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return resolve.Dir(path)
	}
	return resolveFile(path)
}

// resolveFile is resolve.File behind a variable so a test can make it panic.
// The recover in dispatch is the kind of code that is either exercised or
// decorative, and there is no other way to reach it deliberately. Production
// never reassigns this.
var resolveFile = resolve.File

// resolveStack is `composure resolve -json` reached a second way. It calls the
// same loadProject and marshals the same *resolve.Project.
func (s *server) resolveStack(req *rpcRequest) {
	var p resolveParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, "params must be an object with a string \"path\"", nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}

	project, err := loadProject(p.Path, p.Files)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	s.reply(req, project)
}

// resolveErrorData extracts the position from a resolve failure. The position
// is the whole point of returning a typed error here: it is what lets the
// client place a cursor instead of printing prose.
func resolveErrorData(path string, err error) errorData {
	data := errorData{File: path}
	var pe *resolve.ParseError
	if errors.As(err, &pe) {
		if pe.File != "" {
			data.File = pe.File
		}
		if pe.Line > 0 {
			data.Line, data.Column = pe.Line, pe.Column
		}
	}
	var fe *resolve.FileError
	if errors.As(err, &fe) && fe.File != "" {
		data.File = fe.File
	}
	return data
}

// explainStack is `composure explain -json` reached a second way — story 1.10.
//
// It builds the same explainJSON the CLI prints, off the same
// resolve.Explanation, so the editor's "which file set this" popover and the
// command line cannot give two different answers about the same value.
//
// A path that addresses nothing is an ERROR rather than an empty result, and it
// carries the closest paths that do exist in `data` so the client can offer
// them. An empty result would read as "this value has no provenance", which is
// the one thing this model makes impossible.
func (s *server) explainStack(req *rpcRequest) {
	var p explainParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "path" and a string "at"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" && len(p.Files) == 0 {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}
	if strings.TrimSpace(p.At) == "" {
		s.fail(req, codeInvalidParams, `"at" is required: a config path such as services.web.ports[0]`, nil)
		return
	}

	project, err := loadProject(p.Path, p.Files)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	e, err := project.Explain(resolve.ParsePath(p.At))
	if err != nil {
		var pe *resolve.PathError
		if errors.As(err, &pe) {
			s.fail(req, codeNoSuchPath, err.Error(), map[string]any{"path": p.At, "closest": pe.Closest})
			return
		}
		s.fail(req, codeNoSuchPath, err.Error(), map[string]any{"path": p.At})
		return
	}
	s.reply(req, explanationWire(project, e))
}

// topologyStack is `composure topology -json` reached a second way. It resolves
// through the same loadProject the CLI uses — same chain rules, same override
// pickup — and derives with the same topology.Build, so the CLI and the editor
// cannot see two different graphs.
//
// The profile set arrives per request rather than as session state: the graph
// is a pure derivation (AD-6), so toggling a profile is another call with
// another argument, not a mode the server remembers.
func (s *server) topologyStack(req *rpcRequest) {
	var p topologyParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "path" and an optional "profiles" array`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}

	project, err := loadProject(p.Path, p.Files)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	graph, err := topology.Build(project, p.Profiles)
	if err != nil {
		s.fail(req, codeInternal, err.Error(), errorData{File: p.Path})
		return
	}
	s.reply(req, graph)
}

// impactStack is `composure impact -json` reached a second way — story 4.4's focus
// mode.
//
// The set the canvas dims is computed HERE, by topology.BlastRadius, and not in
// the webview from the depends_on edges it was sent. A transitive closure
// written a second time in TypeScript would be a second answer to "what breaks
// if this goes down", and the two would disagree the first time either changed.
//
// A path that names nothing in the graph is an error rather than an empty
// impact: "nothing depends on this" and "this is not in the graph" are
// different answers, and only one of them is about the stack.
func (s *server) impactStack(req *rpcRequest) {
	var p impactParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "path" and a string "at"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}
	if strings.TrimSpace(p.At) == "" {
		s.fail(req, codeInvalidParams, `"at" is required: a config path such as services.db`, nil)
		return
	}

	project, err := loadProject(p.Path, p.Files)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	graph, err := topology.Build(project, p.Profiles)
	if err != nil {
		s.fail(req, codeInternal, err.Error(), errorData{File: p.Path})
		return
	}
	wire, ok := impactOf(graph, p.Path, p.At)
	if !ok {
		s.fail(req, codeNoSuchPath, fmt.Sprintf("%s is not a node in this graph", p.At),
			map[string]any{"path": p.At})
		return
	}
	s.reply(req, wire)
}

// diagnoseStack is `composure diagnose -json` reached a second way. It resolves
// through the same loadProject and diagnoses with the same rules, so the
// editor cannot see a different set of findings than CI does.
//
// Findings come back as a normal result, never as a JSON-RPC error, however
// severe they are (AD-13). Only a file the resolver refuses is an error here,
// and it carries the same position payload stack/resolve does so the client can
// place a cursor.
func (s *server) diagnoseStack(req *rpcRequest) {
	var p topologyParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "path" and an optional "profiles" array`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}

	project, err := loadProject(p.Path, p.Files)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	rep, err := diagnose.AnalyzeProject(p.Path, project, p.Profiles)
	if err != nil {
		s.fail(req, codeInternal, err.Error(), errorData{File: p.Path})
		return
	}
	s.reply(req, rep)
}

// schemaStack is `composure schema -json` reached a second way. It resolves with
// the same resolve.File and describes with the same schema.Inspect, so the
// inspector cannot offer a different set of keys than the CLI reports.
//
// `at` selects one config path; empty means the stack itself, which is what the
// inspector shows when nothing is selected. `all` adds every service and
// resource in one answer — the CLI wants that, the inspector asks per
// selection, because a 500-service project would otherwise ship 46,000 field
// descriptions to redraw one pane.
//
// The `version:` field in the file is not a parameter and cannot become one.
// There is exactly one schema and it is current (AD-20).
func (s *server) schemaStack(req *rpcRequest) {
	var p schemaParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "path" and optional "at" and "all"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}

	project, err := loadProject(p.Path, p.Files)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	env, envKnown := diagnose.Environment(filepath.Dir(p.Path))
	res, err := schema.Inspect(p.Path, project, schema.Options{
		At:       p.At,
		All:      p.All,
		Env:      env,
		EnvKnown: envKnown,
	})
	if err != nil {
		// A path the project does not contain is the client's mistake, not the
		// core's: it names a node that is not there.
		if errors.Is(err, schema.ErrUnknownPath) {
			s.fail(req, codeInvalidParams, err.Error(), errorData{File: p.Path})
			return
		}
		s.fail(req, codeInternal, err.Error(), errorData{File: p.Path})
		return
	}
	s.reply(req, res)
}

// dockerfileStack is `composure dockerfile -json` reached a second way. It calls
// the same dockerfileForm, so the stage form the editor draws is the stage form
// the CLI prints — including which file a `build:` section resolves to.
//
// A Dockerfile that is not there is a RESULT with `missing: true`, never an
// error. Story 6.3: we do not silently omit it, and we do not turn it into a
// failure banner either — the reader asked to see a build's Dockerfile and the
// answer is "that file does not exist", which is information.
func (s *server) dockerfileStack(req *rpcRequest) {
	var p schemaParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "path" and an optional "at"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Path) == "" {
		s.fail(req, codeInvalidParams, `"path" is required`, nil)
		return
	}
	form, err := dockerfileForm(p.Path, p.At)
	if err != nil {
		s.fail(req, codeResolveFailed, err.Error(), resolveErrorData(p.Path, err))
		return
	}
	s.reply(req, form)
}

// editParams is stack/preview's and stack/apply's request: one file and the
// operations to apply to it, in order.
//
// One file, deliberately. A diff the reader is asked to approve has to name the
// file it touches, and a request spanning three files produces three diffs and
// one button.
type editParams struct {
	File string    `json:"file"`
	Ops  []edit.Op `json:"ops"`
}

// editFailureData is what a refused or failed edit carries. `reason` is a
// stable slug — "flow-style", "stale-range", "multi-line" — so a client branches
// on it rather than on the wording of a sentence.
type editFailureData struct {
	File    string `json:"file"`
	Reason  string `json:"reason,omitempty"`
	Written bool   `json:"written"`
}

// editStack is `composure preview` and `composure apply` reached a second way, and
// they are one function apart by the same boolean the CLI uses. The diff the
// editor shows before a write is therefore the diff the write produces; there
// is no second code path for it to differ from.
//
// Nothing is written unless write is true, and write is true only for
// stack/apply — which the extension calls only from an explicit `Save to
// <file>` press. There is no autosave anywhere in this product.
func (s *server) editStack(req *rpcRequest, write bool) {
	var p editParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "file" and an "ops" array`, nil)
			return
		}
	}
	if strings.TrimSpace(p.File) == "" {
		s.fail(req, codeInvalidParams, `"file" is required`, nil)
		return
	}
	if len(p.Ops) == 0 {
		s.fail(req, codeInvalidParams, `"ops" must hold at least one operation`, nil)
		return
	}

	request := edit.Request{File: p.File, Ops: p.Ops}
	var (
		res *edit.Result
		err error
	)
	if write {
		res, err = edit.Apply(request)
	} else {
		res, err = edit.Preview(request)
	}
	if err != nil {
		data := editFailureData{File: p.File, Reason: edit.Reason(err)}
		switch {
		case errors.Is(err, edit.ErrStaleRange):
			s.fail(req, codeStaleRange, err.Error(), data)
		case edit.Refused(err):
			s.fail(req, codeEditRefused, err.Error(), data)
		default:
			s.fail(req, codeEditFailed, err.Error(), data)
		}
		return
	}
	s.reply(req, res)
}

// addParams is stack/add's request — stories 7.3 and 7.4.
//
// `file` is what gets written; `path` is the project the name is checked
// against, which is not the same question. A service declared in an override
// file collides with one added to the base, and the reader is told before
// anything is staged rather than after YAML has silently discarded one of them.
type addParams struct {
	File  string   `json:"file"`
	Path  string   `json:"path,omitempty"`
	Files []string `json:"files,omitempty"`
	Kind  string   `json:"kind"`
	Name  string   `json:"name"`
	Value string   `json:"value,omitempty"`
}

// addResponse is the plan: the operations that perform the declaration.
type addResponse struct {
	Ops []edit.Op `json:"ops"`
}

// addToStack plans a declaration and returns the operations. It writes nothing
// and previews nothing.
//
// The client holds these alongside whatever else the reader has staged and
// sends the lot to stack/apply with one press, so a service and its image are
// one edit and one undo. A method that wrote here would be a second write path
// with its own undo, and the reader would have two of them for one action.
// editableParams is stack/editable's request: one file, and the config paths
// the caller is about to render.
//
// A LIST rather than one path, because the inspector asks about every field in
// a pane at once and one request per field would be ninety round trips to draw
// one service. The answer is per path and independent, so the batch is a
// transport decision and nothing else.
type editableParams struct {
	File  string   `json:"file"`
	At    string   `json:"at,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

// editableResponse is the availability of each path asked about, in the order
// asked. Order is the join: a client that sent a list gets a list back and does
// not have to match on strings it may have normalised.
type editableResponse struct {
	File   string              `json:"file"`
	Fields []edit.Availability `json:"fields"`
}

// editableStack is `composure editable -json` reached a second way — story 5.3's
// provenance answered for the WRITE path rather than the read one.
//
// It exists because the pane may not offer a field the engine would then
// refuse, and may not refuse one without saying why (CLAUDE.md rule 6). The
// classification is the core's, never re-derived in TypeScript: a second answer
// in the webview would disagree with the engine the first time a file used a
// merge key, which is the defect this method was added to close.
func (s *server) editableStack(req *rpcRequest) {
	var p editableParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "file" and "at" or a "paths" array`, nil)
			return
		}
	}
	if strings.TrimSpace(p.File) == "" {
		s.fail(req, codeInvalidParams, `"file" is required`, nil)
		return
	}
	paths := p.Paths
	if strings.TrimSpace(p.At) != "" {
		paths = append([]string{p.At}, paths...)
	}
	if len(paths) == 0 {
		s.fail(req, codeInvalidParams, `"at" or a non-empty "paths" array is required`, nil)
		return
	}
	src, err := os.ReadFile(p.File)
	if err != nil {
		s.fail(req, codeEditFailed, err.Error(), errorData{File: p.File})
		return
	}
	// One read and one parse per path is what Classify does; the file is read
	// once here so a ninety-field pane parses ninety times at worst and reads
	// once. If that ever shows up in a profile, the parse is what to cache —
	// not the answer, which must not outlive the bytes it was computed from.
	out := editableResponse{File: p.File, Fields: make([]edit.Availability, 0, len(paths))}
	for _, at := range paths {
		out.Fields = append(out.Fields, edit.Classify(src, at))
	}
	s.reply(req, out)
}

// extractParams is stack/extract's and stack/extract-apply's request — story
// 9.3, the one operation in this product that writes TWO files.
//
// It is not an entry in `ops` for the reason `editParams` gives for holding one
// file: a request that spans two files produces two diffs, and the one thing
// that must not happen here is a reader approving one of them. The response
// carries both, always.
type extractParams struct {
	File    string `json:"file"`
	At      string `json:"at"`
	Name    string `json:"name,omitempty"`
	EnvFile string `json:"env_file,omitempty"`
	// Story 9.6, revision 9. Both optional, and that is decided rather than
	// inherited: a request carrying neither is applied against the files as
	// they stand, exactly as every call before this one was. A
	// silently-mandatory field would break the client that already shipped.
	//
	// Expect is the compose half's byte range. ExpectEnv is the `.env` half's,
	// and it is a different SHAPE on purpose — the `.env` edit is one appended
	// line with no range to compare, so a byte-range field copied from the
	// single-file contract would compare nothing and pass.
	Expect    *edit.Expect    `json:"expect,omitempty"`
	ExpectEnv *edit.ExpectVar `json:"expect_env,omitempty"`
}

// extractArgParams is stack/extract-arg's and stack/extract-arg-apply's
// request — story 9.4, the Dockerfile half of the same gesture.
//
// It is a separate method rather than a mode of stack/extract because the two
// return different shapes: this one writes ONE file and reports a scope, and
// pretending otherwise would give the client a response whose fields mean
// different things depending on what it sent.
type extractArgParams struct {
	File        string `json:"file"`
	Instruction int    `json:"instruction"`
	Part        string `json:"part,omitempty"`
	Name        string `json:"name,omitempty"`
	// Expect is the substitution's byte range, exactly as everywhere else.
	Expect *edit.Expect `json:"expect,omitempty"`
}

// extractFromStack is `composure extract` reached the second way, one boolean
// apart from its preview exactly as editStack is.
//
// Revision 7 is this method. It was held back deliberately until a client
// called it — a revision exists so that a client with a button and a core
// without the method fails at the handshake rather than in front of the
// reader, and until Epic 9's extension half there was no button.
//
// The ErrStaleRange branch below was INERT until story 9.6: ApplyExtract had no
// code path that could return it, so a handler that looked like it participated
// in the staleness contract did not. It fires now — the compose half through an
// ordinary AD-19 Expect, the `.env` half through an assertion about the
// variable — and a client that sends neither field still gets exactly the
// behaviour it had before.
func (s *server) extractFromStack(req *rpcRequest, write bool) {
	var p extractParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "file" and "at"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.File) == "" {
		s.fail(req, codeInvalidParams, `"file" is required`, nil)
		return
	}
	if strings.TrimSpace(p.At) == "" {
		s.fail(req, codeInvalidParams, `"at" is required`, nil)
		return
	}
	ex := edit.Extract{File: p.File, At: p.At, Name: p.Name, EnvFile: p.EnvFile,
		Expect: p.Expect, ExpectEnv: p.ExpectEnv}
	var (
		res *edit.ExtractResult
		err error
	)
	if write {
		res, err = edit.ApplyExtract(ex)
	} else {
		res, err = edit.PreviewExtract(ex)
	}
	if err != nil {
		data := editFailureData{File: p.File, Reason: edit.Reason(err)}
		switch {
		case errors.Is(err, edit.ErrStaleRange):
			s.fail(req, codeStaleRange, err.Error(), data)
		case edit.Refused(err):
			s.fail(req, codeEditRefused, err.Error(), data)
		default:
			s.fail(req, codeEditFailed, err.Error(), data)
		}
		return
	}
	s.reply(req, res)
}

// extractArgFromStack is `composure extract` on a Dockerfile reached the second
// way — story 9.4 — one boolean apart from its preview, exactly as every other
// write method here.
func (s *server) extractArgFromStack(req *rpcRequest, write bool) {
	var p extractArgParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "file" and an integer "instruction"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.File) == "" {
		s.fail(req, codeInvalidParams, `"file" is required`, nil)
		return
	}
	ex := edit.ExtractArg{File: p.File, Instruction: p.Instruction, Part: p.Part, Name: p.Name, Expect: p.Expect}
	var (
		res *edit.ExtractArgResult
		err error
	)
	if write {
		res, err = edit.ApplyExtractArg(ex)
	} else {
		res, err = edit.PreviewExtractArg(ex)
	}
	if err != nil {
		data := editFailureData{File: p.File, Reason: edit.Reason(err)}
		switch {
		case errors.Is(err, edit.ErrStaleRange):
			s.fail(req, codeStaleRange, err.Error(), data)
		case edit.Refused(err):
			s.fail(req, codeEditRefused, err.Error(), data)
		default:
			s.fail(req, codeEditFailed, err.Error(), data)
		}
		return
	}
	s.reply(req, res)
}

func (s *server) addToStack(req *rpcRequest) {
	var p addParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with "file", "kind" and "name"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.File) == "" {
		s.fail(req, codeInvalidParams, `"file" is required`, nil)
		return
	}

	// Best effort, and deliberately so: the stack a reader most needs to add a
	// service to is the one with nothing in it, and that stack does not resolve
	// (internal/resolve validate requires services). The file's own duplicate
	// check still runs.
	var project *resolve.Project
	if lookup := p.Path; strings.TrimSpace(lookup) != "" || len(p.Files) > 0 {
		if loaded, err := loadProject(lookup, p.Files); err == nil {
			project = loaded
		}
	}

	ops, err := edit.Plan(edit.Add{
		File:   p.File,
		Kind:   p.Kind,
		Name:   p.Name,
		Value:  p.Value,
		Merged: project,
	})
	if err != nil {
		data := editFailureData{File: p.File, Reason: edit.Reason(err)}
		if edit.Refused(err) {
			s.fail(req, codeEditRefused, err.Error(), data)
			return
		}
		s.fail(req, codeEditFailed, err.Error(), data)
		return
	}
	s.reply(req, addResponse{Ops: ops})
}

/* ---- Epic 8: image discovery, the only method that leaves the machine ---- */

// imageParams is image/lookup's and image/search's request.
type imageParams struct {
	// Ref is one image reference, as the file writes it.
	Ref string `json:"ref,omitempty"`
	// Query is a search term.
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// lookupImage is `composure image lookup` reached a second way, through the same
// `lookupImage` function and therefore the same cache, the same guard and the
// same six states.
//
// A NETWORK OUTCOME IS A RESULT, NEVER A JSON-RPC ERROR. That is the whole
// shape of this method. `stack/resolve` errors because a compose file that will
// not parse means the pane has nothing to draw; here the pane has everything to
// draw and is missing one fact, so an error would put a failure banner over a
// panel whose every other statement is still true. The state and the sentence
// travel in the result and the client renders them where the fact would have
// gone (DECISIONS.md 22).
//
// The deadline is the SERVER's. The client bounds its own request too, but a
// core that kept a socket open for a question the editor stopped waiting for is
// exactly the leak this arrangement exists to prevent.
func (s *server) lookupImage(req *rpcRequest) {
	var p imageParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "ref"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Ref) == "" {
		s.fail(req, codeInvalidParams, `"ref" is required: an image reference such as postgres:16-alpine`, nil)
		return
	}
	// The session's context, not a fresh Background: a shutdown arriving while
	// this is outstanding cancels it, so the answer is a `cancelled` state
	// written in milliseconds rather than a socket held open for five seconds
	// past the point anyone is waiting for it.
	ctx, cancel := context.WithTimeout(s.ctx, defaultImageTimeout)
	defer cancel()
	s.reply(req, lookupImage(ctx, p.Ref))
}

// searchImages is `composure image search` reached a second way.
//
// An EMPTY query is a client mistake and is refused as one — it would otherwise
// become a Docker Hub request for nothing, which is a share of a rate limit the
// reader may need for a real question.
func (s *server) searchImages(req *rpcRequest) {
	var p imageParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req, codeInvalidParams, `params must be an object with a string "query"`, nil)
			return
		}
	}
	if strings.TrimSpace(p.Query) == "" {
		s.fail(req, codeInvalidParams, `"query" is required`, nil)
		return
	}
	if p.Limit <= 0 || p.Limit > 50 {
		// Bounded here rather than trusted from the wire: the popup asks for
		// fifteen, and a client asking for five thousand is a bug that would
		// spend somebody's rate limit finding out.
		p.Limit = 15
	}
	ctx, cancel := context.WithTimeout(s.ctx, defaultImageTimeout)
	defer cancel()
	s.reply(req, searchImages(ctx, p.Query, p.Limit))
}

func (s *server) reply(req *rpcRequest, result any) {
	if req.isNotification() {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(s.log, "composure serve: marshal %s result: %v\n", req.Method, err)
		s.fail(req, codeInternal, "result could not be encoded", nil)
		return
	}
	s.write(&rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: raw})
}

func (s *server) fail(req *rpcRequest, code int, msg string, data any) {
	if req.isNotification() {
		fmt.Fprintf(s.log, "composure serve: notification %q failed: %s\n", req.Method, msg)
		return
	}
	s.write(&rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error:   &rpcError{Code: code, Message: msg, Data: data},
	})
}

// write emits one framed response. A marshalling failure is reported as an
// internal error rather than dropped, because a client blocked on a response
// would otherwise hang forever.
//
// It is the ONE place bytes reach stdout, and every caller — reply, fail, the
// framing fault in readFrame, the panic recoveries — goes through it. The lock
// spans BOTH writes: a header emitted by one goroutine and a body by another is
// a frame whose length describes somebody else's bytes, and the client's framer
// treats that as unrecoverable and kills the core.
func (s *server) write(resp *rpcResponse) {
	body, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(s.log, "composure serve: marshal response: %v\n", err)
		body, err = json.Marshal(&rpcResponse{
			JSONRPC: "2.0",
			ID:      resp.ID,
			Error:   &rpcError{Code: codeInternal, Message: "response could not be encoded"},
		})
		if err != nil {
			return
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		fmt.Fprintf(s.log, "composure serve: write header: %v\n", err)
		return
	}
	if _, err := s.out.Write(body); err != nil {
		fmt.Fprintf(s.log, "composure serve: write body: %v\n", err)
	}
}
