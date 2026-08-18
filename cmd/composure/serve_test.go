package main

// These tests drive the real server over real pipes and speak the real wire
// format. A mock of the protocol would test the mock: the framing, the
// truncation behaviour and the error payloads are the product here.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elzouhery/composure/internal/resolve"
)

// session is a client on the other end of the server's pipes.
type session struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Reader
	logs *lockedBuffer

	done chan int
	// over is closed when serve has returned AND drained. `done` cannot serve
	// this purpose: waitExit consumes it, and a cleanup that waited on it a
	// second time would block forever.
	over chan struct{}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs serve in a goroutine with pipes for stdin and stdout.
func start(t *testing.T) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{
		t:    t,
		in:   inW,
		out:  bufio.NewReader(outR),
		logs: &lockedBuffer{},
		done: make(chan int, 1),
		over: make(chan struct{}),
	}
	go func() {
		code := serve(inR, outW, s.logs)
		// Closing the write end unblocks a client still reading.
		_ = outW.Close()
		_ = inR.Close()
		s.done <- code
		close(s.over)
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
		// AND WAIT FOR THE SERVER TO GO. Handlers now run off the read loop, so
		// a session left running is a set of goroutines still reading the
		// package-level hooks (`resolveFile`, `imageClient`) that the next
		// cleanup is about to restore. Without this join the race detector
		// reports that collision against whichever test happens to run next,
		// which is the least useful place it could possibly be reported.
		select {
		case <-s.over:
		case <-time.After(10 * time.Second):
			t.Error("the server did not stop when its input closed")
		}
	})
	return s
}

// send writes a correctly framed message.
func (s *session) send(body string) {
	s.t.Helper()
	s.sendRaw(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
}

// sendRaw writes bytes verbatim, framing included or not.
func (s *session) sendRaw(raw string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, raw); err != nil {
		s.t.Fatalf("write to server: %v", err)
	}
}

// closeInput closes the client's write end, which is what a client exiting
// looks like from the server's side.
func (s *session) closeInput() {
	s.t.Helper()
	_ = s.in.Close()
}

// read reads one framed response, failing the test on a timeout so a hang is
// reported as a hang rather than as a stalled suite.
func (s *session) read() rawResponse {
	s.t.Helper()
	type result struct {
		resp rawResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := readFrameFrom(s.out)
		ch <- result{r, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			s.t.Fatalf("read response: %v", r.err)
		}
		return r.resp
	case <-time.After(10 * time.Second):
		s.t.Fatal("timed out waiting for a response")
		return rawResponse{}
	}
}

// waitExit waits for the server loop to return and reports the exit code.
func (s *session) waitExit() int {
	s.t.Helper()
	select {
	case code := <-s.done:
		return code
	case <-time.After(10 * time.Second):
		s.t.Fatal("server did not exit")
		return -1
	}
}

type rawResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

// readFrameFrom is an independent client-side implementation of the framing,
// so a bug in the server's reader cannot cancel out a bug in its writer.
func readFrameFrom(r *bufio.Reader) (rawResponse, error) {
	var out rawResponse
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return out, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return out, fmt.Errorf("malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return out, fmt.Errorf("bad Content-Length %q", value)
			}
		}
	}
	if length < 0 {
		return out, errors.New("response frame had no Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("response body is not JSON: %v: %s", err, body)
	}
	return out, nil
}

// waitForLog blocks until a line containing `want` has reached stderr.
//
// A NOTIFICATION IS ANSWERED WITH NOTHING, so its only observable effect is the
// stderr line — and since a handler runs off the read loop, the next response
// off the wire is no longer proof the notification has been handled. This is
// the join that used to be implicit in the ordering. It is also what gives the
// race detector a happens-before edge for the `resolveFile` hook the panic
// tests install: `logs` is mutex-guarded, so reading it here orders the test's
// cleanup after the handler that read the hook.
func (s *session) waitForLog(want string) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.logs.String(), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	s.t.Fatalf("stderr never mentioned %q: %q", want, s.logs.String())
}

func (s *session) handshake(id int) rawResponse {
	s.t.Helper()
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"initialize","params":{}}`, id))
	return s.read()
}

const composeFixture = `services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
  db:
    image: postgres:16
`

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestServeHandshake(t *testing.T) {
	s := start(t)
	resp := s.handshake(1)

	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("initialize returned an error: %+v", resp.Error)
	}
	var got initializeResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if got.ServerName != "composure" {
		t.Errorf("serverName = %q, want composure", got.ServerName)
	}
	if got.Protocol != protocolRevision {
		t.Errorf("protocol = %d, want %d", got.Protocol, protocolRevision)
	}
	if got.Version == "" {
		t.Error("version is empty")
	}
}

// The id must come back exactly as it was sent, including a string id — a
// client correlating on `"a"` cannot match a response carrying 0.
func TestServePreservesStringID(t *testing.T) {
	s := start(t)
	s.send(`{"jsonrpc":"2.0","id":"a-1","method":"initialize"}`)
	if got := string(s.read().ID); got != `"a-1"` {
		t.Errorf("id = %s, want \"a-1\"", got)
	}
}

// stack/resolve must be `composure resolve -json` reached a second way, not a
// second implementation. Both paths marshal the same *resolve.Project, so the
// bytes are comparable directly.
func TestServeResolveMatchesCLIOutput(t *testing.T) {
	path := writeFixture(t, "compose.yaml", composeFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/resolve returned an error: %+v", resp.Error)
	}

	// The same call the CLI makes, marshalled the same way.
	want := resolveJSONViaLibrary(t, path)
	if !jsonEqual(t, resp.Result, want) {
		t.Errorf("RPC payload differs from the library marshaller\n rpc: %s\n lib: %s", resp.Result, want)
	}

	// The comparison above proves the two surfaces agree with each other. It
	// cannot prove either agrees with the client: renaming a JSON tag moves
	// both sides at once and the test stays green. TestServeResolveWireKeys
	// is the assertion that actually holds the contract.
}

// The literal wire keys the TypeScript client reads.
//
// extension/host/project.ts navigates this payload by name — `root`, `kind`,
// `map`, `key`, `key_origin`, `value`, `seq`, `scalar`, and the four fields of
// an origin. None of those names appears in a Go identifier, so nothing in the
// Go build fails when one changes; the panel simply draws no services. The
// assertions below are deliberately made against a decoded map rather than a
// struct, so a struct tag cannot supply the name it is meant to be checked
// against.
func TestServeResolveWireKeys(t *testing.T) {
	path := writeFixture(t, "compose.yaml", composeFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/resolve returned an error: %+v", resp.Error)
	}

	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	// files[].path — the provenance the whole product is built on.
	files, ok := payload["files"].([]any)
	if !ok || len(files) == 0 {
		t.Fatalf(`result has no "files" array: %s`, resp.Result)
	}
	first, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("files[0] is not an object: %v", files[0])
	}
	if p, ok := first["path"].(string); !ok || p == "" {
		t.Errorf(`files[0] has no string "path": %v`, first)
	}

	root, ok := payload["root"].(map[string]any)
	if !ok {
		t.Fatalf(`result has no "root" object: %s`, resp.Result)
	}
	// The client switches on this exact string. "map"/"dict"/"object" all fail.
	if kind, _ := root["kind"].(string); kind != "mapping" {
		t.Errorf(`root.kind = %q, want "mapping"`, root["kind"])
	}

	services := mapEntry(t, root, "services")
	servicesValue, ok := services["value"].(map[string]any)
	if !ok {
		t.Fatalf(`services entry has no "value" object: %v`, services)
	}
	if kind, _ := servicesValue["kind"].(string); kind != "mapping" {
		t.Errorf(`services value kind = %q, want "mapping"`, servicesValue["kind"])
	}

	// The service names, in file order — the exact projection serviceNodes takes.
	entries, ok := servicesValue["map"].([]any)
	if !ok {
		t.Fatalf(`services value has no "map" array: %v`, servicesValue)
	}
	var names []string
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("service entry is not an object: %v", e)
		}
		name, ok := entry["key"].(string)
		if !ok {
			t.Fatalf(`service entry has no string "key": %v`, entry)
		}
		names = append(names, name)
		if _, ok := entry["value"].(map[string]any); !ok {
			t.Errorf(`service %q has no "value" object`, name)
		}
		// key_origin, not keyOrigin: the node is placed from this.
		origin, ok := entry["key_origin"].(map[string]any)
		if !ok {
			t.Fatalf(`service %q has no "key_origin" object: %v`, name, entry)
		}
		assertOrigin(t, fmt.Sprintf("services.%s key_origin", name), origin)
	}
	if !reflect.DeepEqual(names, []string{"web", "db"}) {
		t.Errorf("service names = %v, want [web db] in file order", names)
	}

	// image is read as `.scalar` off a value whose kind is exactly "scalar".
	web := mapEntry(t, servicesValue, "web")
	webValue, _ := web["value"].(map[string]any)
	image := mapEntry(t, webValue, "image")
	imageValue, ok := image["value"].(map[string]any)
	if !ok {
		t.Fatalf(`image entry has no "value" object: %v`, image)
	}
	if kind, _ := imageValue["kind"].(string); kind != "scalar" {
		t.Errorf(`image value kind = %q, want "scalar"`, imageValue["kind"])
	}
	if got, _ := imageValue["scalar"].(string); got != "nginx:1.27" {
		t.Errorf(`image .scalar = %q, want "nginx:1.27"`, imageValue["scalar"])
	}
	origin, ok := imageValue["origin"].(map[string]any)
	if !ok {
		t.Fatalf(`image value has no "origin" object: %v`, imageValue)
	}
	assertOrigin(t, "services.web.image origin", origin)

	// ports is counted off `.seq`, so the key has to be that.
	ports := mapEntry(t, webValue, "ports")
	portsValue, ok := ports["value"].(map[string]any)
	if !ok {
		t.Fatalf(`ports entry has no "value" object: %v`, ports)
	}
	if kind, _ := portsValue["kind"].(string); kind != "sequence" {
		t.Errorf(`ports value kind = %q, want "sequence"`, portsValue["kind"])
	}
	seq, ok := portsValue["seq"].([]any)
	if !ok {
		t.Fatalf(`ports value has no "seq" array: %v`, portsValue)
	}
	if len(seq) != 1 {
		t.Errorf("ports seq has %d entries, want 1", len(seq))
	}
}

// mapEntry finds one `{key, key_origin, value}` entry of a mapping value by its
// key, asserting the entry keys as it goes.
func mapEntry(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	entries, ok := value["map"].([]any)
	if !ok {
		t.Fatalf(`value has no "map" array while looking for %q: %v`, key, value)
	}
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("map entry is not an object: %v", e)
		}
		if k, _ := entry["key"].(string); k == key {
			if _, ok := entry["key_origin"]; !ok {
				t.Errorf(`entry %q has no "key_origin"`, key)
			}
			if _, ok := entry["value"]; !ok {
				t.Errorf(`entry %q has no "value"`, key)
			}
			return entry
		}
	}
	t.Fatalf("no map entry named %q in %v", key, value)
	return nil
}

// assertOrigin checks the four fields R1.8 requires of every resolved value.
// `step` is present even when it is zero, because the client reads it
// unconditionally.
func assertOrigin(t *testing.T, what string, origin map[string]any) {
	t.Helper()
	if file, ok := origin["file"].(string); !ok || file == "" {
		t.Errorf(`%s has no string "file": %v`, what, origin)
	}
	line, ok := origin["line"].(float64)
	if !ok || line <= 0 {
		t.Errorf(`%s has no positive "line": %v`, what, origin)
	}
	column, ok := origin["column"].(float64)
	if !ok || column <= 0 {
		t.Errorf(`%s has no positive "column": %v`, what, origin)
	}
	if _, ok := origin["step"]; !ok {
		t.Errorf(`%s has no "step" — the merge step is what makes provenance answerable: %v`, what, origin)
	}
}

func resolveJSONViaLibrary(t *testing.T, path string) []byte {
	t.Helper()
	project, err := resolve.File(path)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	b, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	return b
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("left side is not JSON: %v", err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("right side is not JSON: %v", err)
	}
	return reflect.DeepEqual(x, y)
}

// A malformed file must reach the client as a JSON-RPC error carrying the
// position. Prose on stderr would throw away the one thing that lets an editor
// place a cursor.
func TestServeResolveMalformedCarriesPosition(t *testing.T) {
	path := writeFixture(t, "compose.yaml", "services:\n  web:\n    ports: [\"8080:80\"\n")

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":{"path":%q}}`, path))
	resp := s.read()

	if resp.Error == nil {
		t.Fatalf("malformed file resolved without error: %s", resp.Result)
	}
	if resp.Error.Code != codeResolveFailed {
		t.Errorf("code = %d, want %d", resp.Error.Code, codeResolveFailed)
	}
	var data errorData
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("decode error data: %v (data=%s)", err, resp.Error.Data)
	}
	if data.File != path {
		t.Errorf("data.file = %q, want %q", data.File, path)
	}
	if data.Line <= 0 {
		t.Errorf("data.line = %d, want a real position; without it no cursor can be placed", data.Line)
	}
	// The stream survives a failed resolve: the next request is answered.
	s.send(`{"jsonrpc":"2.0","id":3,"method":"initialize"}`)
	if got := string(s.read().ID); got != "3" {
		t.Errorf("after a failed resolve, id = %s, want 3", got)
	}
}

// A file that does not exist has no position. The error must still name the
// file, and must not invent a line.
func TestServeResolveMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":{"path":%q}}`, path))
	resp := s.read()

	if resp.Error == nil {
		t.Fatal("a missing file resolved without error")
	}
	var data errorData
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("decode error data: %v", err)
	}
	if data.File != path {
		t.Errorf("data.file = %q, want %q", data.File, path)
	}
	if data.Line != 0 {
		t.Errorf("data.line = %d, want 0 — there is no position to report", data.Line)
	}
}

// A panic below the dispatch must end the request, not the session.
//
// The client holds one core per window and draws every compose file in the
// workspace through it. If a single adversarial file could take the process
// down, the reader would lose the panel — and every other in-flight call with
// it — for a fault local to one request. The panic has to reach the wire as an
// internal error, and the session has to keep answering afterwards.
func TestServeResolvePanicDoesNotEndTheSession(t *testing.T) {
	original := resolveFile
	resolveFile = func(string) (*resolve.Project, error) {
		panic("engineered panic inside resolve")
	}
	t.Cleanup(func() { resolveFile = original })

	path := writeFixture(t, "compose.yaml", composeFixture)
	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":{"path":%q}}`, path))

	resp := s.read()
	if resp.Error == nil {
		t.Fatalf("a panic produced a successful result: %s", resp.Result)
	}
	if resp.Error.Code != codeInternal {
		t.Errorf("code = %d, want %d", resp.Error.Code, codeInternal)
	}
	if string(resp.ID) != "2" {
		t.Errorf("id = %s, want 2 — the client cannot correlate this failure", resp.ID)
	}
	// The stack belongs on stderr, where it is useful, and nowhere near stdout.
	if !strings.Contains(s.logs.String(), "panic") {
		t.Errorf("the panic was not reported on stderr: %q", s.logs.String())
	}

	// The session survives: the next request is answered normally.
	resolveFile = original
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"stack/resolve","params":{"path":%q}}`, path))
	next := s.read()
	if next.Error != nil {
		t.Fatalf("the session did not survive the panic: %+v", next.Error)
	}
	if string(next.ID) != "3" {
		t.Errorf("id = %s, want 3", next.ID)
	}
}

// A panic on a notification has no id to answer. It must still be contained.
func TestServeResolvePanicOnNotification(t *testing.T) {
	original := resolveFile
	resolveFile = func(string) (*resolve.Project, error) {
		panic("engineered panic inside resolve")
	}
	t.Cleanup(func() { resolveFile = original })

	path := writeFixture(t, "compose.yaml", composeFixture)
	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"stack/resolve","params":{"path":%q}}`, path))
	// Nothing is written for a notification, so the proof the loop lived is
	// that the next request comes back — and comes back as the FIRST frame,
	// which is also what proves nothing else reached stdout.
	s.send(`{"jsonrpc":"2.0","id":8,"method":"initialize"}`)
	if got := string(s.read().ID); got != "8" {
		t.Fatalf("id = %s, want 8 — a panicking notification broke the stream", got)
	}
	// The panic is contained on the handler's own goroutine, so it is reported
	// on stderr rather than inferred from the stream surviving.
	s.waitForLog("panic")
}

func TestServeResolveInvalidParams(t *testing.T) {
	for _, tc := range []struct {
		name, params string
	}{
		{"missing path", `{}`},
		{"empty path", `{"path":"  "}`},
		{"params not an object", `"compose.yaml"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t)
			s.handshake(1)
			s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":%s}`, tc.params))
			resp := s.read()
			if resp.Error == nil {
				t.Fatalf("expected an error, got result %s", resp.Result)
			}
			if resp.Error.Code != codeInvalidParams {
				t.Errorf("code = %d, want %d", resp.Error.Code, codeInvalidParams)
			}
		})
	}
}

func TestServeUnknownMethod(t *testing.T) {
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"stack/enhance"}`)
	resp := s.read()
	if resp.Error == nil {
		t.Fatal("unknown method did not error")
	}
	if resp.Error.Code != codeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, codeMethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "stack/enhance") {
		t.Errorf("message %q does not name the method", resp.Error.Message)
	}
}

func TestServeRejectsWrongProtocolVersion(t *testing.T) {
	s := start(t)
	s.send(`{"jsonrpc":"1.0","id":1,"method":"initialize"}`)
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeInvalidRequest {
		t.Fatalf("want invalid-request, got %+v / %s", resp.Error, resp.Result)
	}
}

// A body that is not JSON is a client fault the stream survives: the framing
// was intact, so the length was known and the next frame starts where it says.
func TestServeMalformedBodyKeepsStreamAlive(t *testing.T) {
	s := start(t)
	s.send(`{"jsonrpc":"2.0",`)
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeParse {
		t.Fatalf("want a parse error, got %+v", resp.Error)
	}
	if string(resp.ID) != "null" {
		t.Errorf("id = %s, want null — there was no id to echo", resp.ID)
	}
	if got := string(s.handshake(7).ID); got != "7" {
		t.Errorf("stream did not survive a bad body: id = %s", got)
	}
}

// Broken framing is not recoverable: after a bad Content-Length the position of
// every following byte is unknown. The server must report and stop rather than
// guess where the next frame begins.
func TestServeBrokenFramingStops(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"no content-length", "X-Nonsense: 1\r\n\r\n{}"},
		{"non-numeric length", "Content-Length: banana\r\n\r\n{}"},
		{"malformed header line", "Content-Length 12\r\n\r\n{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t)
			s.sendRaw(tc.raw)
			resp := s.read()
			if resp.Error == nil || resp.Error.Code != codeParse {
				t.Fatalf("want a parse error, got %+v", resp.Error)
			}
			if code := s.waitExit(); code == 0 {
				t.Error("server exited 0 after broken framing; a desynchronised stream is a failure")
			}
			if s.logs.String() == "" {
				t.Error("nothing was logged to stderr about the broken framing")
			}
		})
	}
}

// A half-written frame must block rather than parse as a whole message, and an
// EOF inside one must be reported — never treated as a complete request.
func TestServeHalfWrittenFrame(t *testing.T) {
	s := start(t)
	s.handshake(1)

	full := `{"jsonrpc":"2.0","id":2,"method":"initialize"}`
	// Announce the whole message, deliver half of it, then vanish.
	s.sendRaw(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(full), full[:len(full)/2]))
	s.closeInput()

	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeParse {
		t.Fatalf("want a parse error for the truncated frame, got %+v / %s", resp.Error, resp.Result)
	}
	if string(resp.ID) != "null" {
		t.Errorf("id = %s, want null — the id was never fully received", resp.ID)
	}
	if code := s.waitExit(); code == 0 {
		t.Error("server exited 0 on a truncated frame")
	}
	if !strings.Contains(s.logs.String(), "truncated") {
		t.Errorf("stderr does not name the truncation: %q", s.logs.String())
	}
}

// A header block that ends mid-way is the same failure seen one layer earlier.
func TestServeTruncatedHeaderBlock(t *testing.T) {
	s := start(t)
	s.sendRaw("Content-Length: 2")
	s.closeInput()
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeParse {
		t.Fatalf("want a parse error, got %+v", resp.Error)
	}
	if code := s.waitExit(); code == 0 {
		t.Error("server exited 0 on a truncated header block")
	}
}

func TestServeShutdownThenExit(t *testing.T) {
	s := start(t)
	s.handshake(1)

	s.send(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`)
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("shutdown errored: %+v", resp.Error)
	}
	// JSON-RPC requires exactly one of result and error: a null result is a
	// present field, not an absent one.
	if string(resp.Result) != "null" {
		t.Errorf("shutdown result = %q, want null", resp.Result)
	}

	s.send(`{"jsonrpc":"2.0","method":"exit"}`)
	if code := s.waitExit(); code != 0 {
		t.Errorf("exit after shutdown gave %d, want 0", code)
	}
}

// `exit` without a preceding `shutdown` is an unclean stop. A supervisor has to
// be able to tell the two apart.
func TestServeExitWithoutShutdown(t *testing.T) {
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","method":"exit"}`)
	if code := s.waitExit(); code != 1 {
		t.Errorf("bare exit gave %d, want 1", code)
	}
}

// A client that dies without shutting down is a crash, not a clean close.
func TestServeInputClosedWithoutShutdown(t *testing.T) {
	s := start(t)
	s.handshake(1)
	s.closeInput()
	if code := s.waitExit(); code != 1 {
		t.Errorf("closed input gave %d, want 1", code)
	}
	if !strings.Contains(s.logs.String(), "before shutdown") {
		t.Errorf("stderr does not explain the exit: %q", s.logs.String())
	}
}

func TestServeInputClosedAfterShutdown(t *testing.T) {
	s := start(t)
	s.send(`{"jsonrpc":"2.0","id":1,"method":"shutdown"}`)
	s.read()
	s.closeInput()
	if code := s.waitExit(); code != 0 {
		t.Errorf("closed input after shutdown gave %d, want 0", code)
	}
}

// A notification is a request with no id and must never be answered — a stray
// response would be correlated against nothing and, worse, would be read as the
// answer to the next request.
func TestServeNotificationGetsNoResponse(t *testing.T) {
	s := start(t)
	s.send(`{"jsonrpc":"2.0","method":"stack/resolve","params":{}}`)
	s.send(`{"jsonrpc":"2.0","id":9,"method":"initialize"}`)
	resp := s.read()
	if string(resp.ID) != "9" {
		t.Fatalf("id = %s, want 9 — a notification was answered", resp.ID)
	}
	s.waitForLog("stack/resolve")
}

// A `null` id is not an id. Same rule as an absent one.
func TestServeNullIDIsANotification(t *testing.T) {
	s := start(t)
	s.send(`{"jsonrpc":"2.0","id":null,"method":"initialize"}`)
	s.send(`{"jsonrpc":"2.0","id":4,"method":"initialize"}`)
	if got := string(s.read().ID); got != "4" {
		t.Errorf("id = %s, want 4", got)
	}
}

// Compose values contain newlines. Newline-delimited JSON would split this
// payload; the framing must not.
func TestServeResolveMultilineValues(t *testing.T) {
	path := writeFixture(t, "compose.yaml", "services:\n  web:\n    image: nginx\n    command: |\n      one\n      two\n      three\n")

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/resolve","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("resolve errored: %+v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), `one\ntwo\nthree`) {
		t.Errorf("multi-line scalar did not survive the frame: %s", resp.Result)
	}
}

// Several requests on one connection, in order, each answered once. This is the
// shape of a real session: one client, many resolves.
func TestServeSequentialRequests(t *testing.T) {
	path := writeFixture(t, "compose.yaml", composeFixture)
	s := start(t)
	s.handshake(1)
	for i := 2; i < 6; i++ {
		s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"stack/resolve","params":{"path":%q}}`, i, path))
		resp := s.read()
		if resp.Error != nil {
			t.Fatalf("request %d errored: %+v", i, resp.Error)
		}
		if string(resp.ID) != strconv.Itoa(i) {
			t.Fatalf("id = %s, want %d — responses are out of order", resp.ID, i)
		}
	}
}

// stdout carries the protocol only. Anything else on it breaks the framing for
// every message that follows.
func TestServeWritesNothingButFramesToStdout(t *testing.T) {
	path := writeFixture(t, "compose.yaml", composeFixture)
	s := start(t)
	s.handshake(1)
	// Provoke every logging path there is, then check the frame stream is
	// still exactly one response per request.
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"stack/resolve","params":{"path":%q}}`, path)) // notification
	s.send(`{"jsonrpc":"2.0","id":2,"method":"nope"}`)
	if got := string(s.read().ID); got != "2" {
		t.Fatalf("id = %s, want 2 — something other than a frame reached stdout", got)
	}
	s.send(`{"jsonrpc":"2.0","id":3,"method":"shutdown"}`)
	if got := string(s.read().ID); got != "3" {
		t.Fatalf("id = %s, want 3", got)
	}
	s.send(`{"jsonrpc":"2.0","method":"exit"}`)
	if code := s.waitExit(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
