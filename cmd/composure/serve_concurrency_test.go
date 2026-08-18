package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elzouhery/composure/internal/hub"
	"github.com/elzouhery/composure/internal/resolve"
)

// Epic 8 was allowed to break this product's central invariant — that every
// answer is a pure function of files on disk — on one condition, recorded as
// DECISIONS.md 22: a lookup must never block, slow or empty the pane.
//
// The extension holds up its half. It starts the lookup unawaited and
// generation-guards the answer, so a slow reply cannot stall a render. But the
// core serialises: serve() is `for { readFrame(); dispatch(); }` with no
// goroutine in the file, and dispatch runs the handler inline. Every method
// before Epic 8 was a fast, file-local function, so that was invisible and
// correct. `image/lookup` is the first one that can take seconds.
//
// So a reader who selects a service while a lookup is outstanding waits behind
// the network — and the extension's mitigation cannot help, because the second
// request has already been sent and is sitting in the core's queue.
//
// This test does not mock the serialisation away. It blocks a real Docker Hub
// handler, sends a real second request on the same connection, and asks which
// answer comes back first.
func TestASlowLookupDoesNotBlockTheNextRequest(t *testing.T) {
	// A Hub that never answers until the test lets it. Nothing here opens a
	// socket to Docker Hub: the client is pointed at loopback, exactly as
	// newHubStub does.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	t.Cleanup(srv.Close)
	// Registered AFTER srv.Close so it runs BEFORE it: cleanups are LIFO, and
	// httptest's Close waits for outstanding handlers. A handler parked on
	// `release` would otherwise hang the suite rather than fail this test.
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	// A FRESH CACHE, restored afterwards, exactly as newHubStub does. The cache
	// is process-wide by design (R6.8), so without this the parked handler is
	// never reached on a second run — the answer comes from the first run's
	// entry, the lookup is instant, and the test passes for the one reason it
	// must never pass: no network call was outstanding at all.
	old, oldCache := imageClient, imageCache
	imageCache = hub.NewCache(clientLister{}, imageCacheTTL)
	t.Cleanup(func() { imageCache = oldCache })
	imageClient = &hub.Client{
		HTTP:            srv.Client(),
		SearchURL:       srv.URL + "/v4/search",
		LegacySearchURL: srv.URL + "/v1/search",
		TagsURL:         srv.URL + "/v2/repositories/%s/tags/",
	}
	t.Cleanup(func() { imageClient = old })

	compose := writeFixture(t, "compose.yaml", "services:\n  web:\n    image: nginx:1.27-alpine\n")

	s := start(t)
	s.handshake(1)

	// Request 2 goes to the network and will not be answered until `release`.
	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{"ref":"postgres:16-alpine"}}`)

	// Request 3 is a pure function of a file on disk. It cannot legitimately
	// wait on Docker Hub for anything.
	//
	// Sent from a goroutine because the send ITSELF is part of what is broken:
	// stdin is an unbuffered pipe, and a serialising server is not reading
	// while it handles request 2 — so the client cannot even deliver the
	// second request, let alone have it answered. Blocking the test's own
	// write would hang this test instead of failing it.
	go s.send(`{"jsonrpc":"2.0","id":3,"method":"stack/schema","params":` +
		mustJSON(map[string]any{"path": compose, "at": ""}) + `}`)

	// The first answer off the wire must be request 3. JSON-RPC does not
	// promise responses in request order and the client correlates by id, so
	// answering out of order is legitimate — answering in order here means the
	// file read waited on the network.
	first := make(chan json.RawMessage, 1)
	go func() {
		resp := s.read()
		first <- resp.ID
	}()

	select {
	case id := <-first:
		var got int
		if err := json.Unmarshal(id, &got); err != nil {
			t.Fatalf("first response has a malformed id %s: %v", id, err)
		}
		if got != 3 {
			t.Fatalf("the first answer was id %d, want 3 — the file read waited behind the network call", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer in 3s while a lookup was outstanding: the server is serialising, so a Docker Hub call blocks every other request")
	}

	unblock()

	// The lookup is still owed an answer, and it must still get one: overlapping
	// is not the same as dropping. Reading it here is also what lets `imageClient`
	// be restored safely — a cleanup that swaps the client out from under a
	// handler still parked in the stub is a data race the detector reports
	// against this test rather than against the server.
	if got := string(s.read().ID); got != "2" {
		t.Fatalf("the lookup answered with id %s, want 2", got)
	}
}

// mustJSON is a small helper so the request bodies above stay readable.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

/* ---- the rest of what concurrency has to hold -------------------------- */

// parkedHub is the blocking Docker Hub above, as a helper, because the cleanup
// ORDER is the part that is easy to get wrong: httptest's Close waits for
// outstanding handlers, so a handler parked on the release channel hangs the
// suite for the test timeout instead of failing a test. Cleanups run LIFO, so
// the unblock must be registered AFTER srv.Close.
//
// Nothing here opens a socket to Docker Hub. The client is pointed at loopback
// and the cache is replaced, so a parked handler is really reached rather than
// answered from an earlier test's entry.
type parkedHub struct {
	unblock func()
	entered chan struct{}
}

func newParkedHub(t *testing.T) *parkedHub {
	t.Helper()
	release := make(chan struct{})
	h := &parkedHub{entered: make(chan struct{}, 32)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case h.entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			// The core cancelled the request — a shutdown, typically. Answering
			// nothing is what a cancelled call looks like from here.
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	t.Cleanup(srv.Close)
	var once sync.Once
	h.unblock = func() { once.Do(func() { close(release) }) }
	t.Cleanup(h.unblock)

	oldClient, oldCache := imageClient, imageCache
	imageCache = hub.NewCache(clientLister{}, imageCacheTTL)
	imageClient = &hub.Client{
		HTTP:            srv.Client(),
		SearchURL:       srv.URL + "/v4/search",
		LegacySearchURL: srv.URL + "/v1/search",
		TagsURL:         srv.URL + "/v2/repositories/%s/tags/",
	}
	t.Cleanup(func() { imageClient, imageCache = oldClient, oldCache })
	return h
}

// waitEntered blocks until a request has actually reached the parked handler.
// Sending the next request before that would prove nothing: the lookup might
// not have left the core yet.
func (h *parkedHub) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no request reached the stub Hub in 5s")
	}
}

// The table that decides what may overlap what. It is asserted directly because
// it is the whole safety argument of this change, and because the difference
// between `stack/apply` on the read plane and on the write plane is invisible
// in every other test until two saves land in the same millisecond.
func TestPlanesSayWhatMayOverlap(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   plane
		known  bool
	}{
		// The two methods that WRITE FILES. Exclusive of each other and of
		// every read: edit.Apply reads the file, splices the buffer and writes
		// it back, so an overlapping writer computes its bytes from a document
		// that no longer exists.
		{"stack/apply", planeWrite, true},
		{"stack/extract-apply", planeWrite, true},

		// Reads. They may overlap each other freely — they hold no state and
		// write no bytes — but never a write.
		{"stack/resolve", planeRead, true},
		{"stack/explain", planeRead, true},
		{"stack/topology", planeRead, true},
		{"stack/impact", planeRead, true},
		{"stack/diagnose", planeRead, true},
		{"stack/schema", planeRead, true},
		{"stack/dockerfile", planeRead, true},
		{"stack/preview", planeRead, true},
		{"stack/add", planeRead, true},
		{"stack/editable", planeRead, true},
		{"stack/extract", planeRead, true},

		// The network. No lock at all, which is the point of the change.
		{"image/lookup", planeNetwork, true},
		{"image/search", planeNetwork, true},

		// Control, and the unknown method that is answered as one.
		{"initialize", planeControl, false},
		{"shutdown", planeControl, false},
		{"exit", planeControl, false},
		{"stack/nonesuch", planeControl, false},
	} {
		got, known := planeOf(tc.method)
		if known != tc.known {
			t.Errorf("planeOf(%q) known = %v, want %v", tc.method, known, tc.known)
		}
		if known && got != tc.want {
			t.Errorf("planeOf(%q) = %d, want %d", tc.method, got, tc.want)
		}
	}
}

// Every response goes out one stdout, and a frame is a header write followed by
// a body write. Two goroutines interleaving those pairs produce a
// `Content-Length` that describes somebody else's bytes — which the client's
// framer cannot resynchronise from, so it kills the core rather than showing a
// wrong answer.
//
// The client here decodes with the SAME independent framer the rest of
// serve_test.go uses, so interleaving shows up as a decode failure rather than
// as a subtly wrong payload. The bodies are large on purpose: a short frame
// might survive a race by accident.
func TestConcurrentAnswersNeverInterleaveOnTheWire(t *testing.T) {
	var b strings.Builder
	b.WriteString("services:\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "  svc%02d:\n    image: nginx:1.27\n    environment:\n      LONG: %s\n",
			i, strings.Repeat("x", 400))
	}
	path := writeFixture(t, "compose.yaml", b.String())

	s := start(t)
	s.handshake(1)

	const n = 24
	go func() {
		for id := 2; id < 2+n; id++ {
			method, params := "stack/resolve", mustJSON(map[string]any{"path": path})
			if id%3 == 0 {
				method, params = "stack/schema", mustJSON(map[string]any{"path": path, "all": true})
			}
			s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
		}
	}()

	seen := map[int]int{}
	for i := 0; i < n; i++ {
		resp := s.read()
		if resp.Error != nil {
			t.Fatalf("request errored: %+v", resp.Error)
		}
		var id int
		if err := json.Unmarshal(resp.ID, &id); err != nil {
			t.Fatalf("malformed id %s: %v", resp.ID, err)
		}
		// Correlation is by ID and nothing else — JSON-RPC promises no order,
		// and `extension/host/core.ts` keys its pending map on the id.
		var body map[string]any
		if err := json.Unmarshal(resp.Result, &body); err != nil {
			t.Fatalf("id %d carried a body that is not an object — the frames interleaved: %v", id, err)
		}
		seen[id]++
	}
	for id := 2; id < 2+n; id++ {
		if seen[id] != 1 {
			t.Errorf("id %d was answered %d times, want exactly once", id, seen[id])
		}
	}
}

// A shutdown while a lookup is outstanding must lose nothing and hang nothing.
//
// The extension's dispose() sends `shutdown` and then `exit` back to back
// WITHOUT waiting, so this is not a hypothetical sequence — it is what closing
// a window does. The lookup's context is the session's, so `exit` cancels it and
// the answer comes back as the `cancelled` state rather than five seconds later
// or not at all.
func TestShutdownWithALookupOutstandingLosesNothing(t *testing.T) {
	h := newParkedHub(t)
	s := start(t)
	s.handshake(1)

	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{"ref":"postgres:16-alpine"}}`)
	h.waitEntered(t)

	// The shutdown is answered while the lookup is still parked. It is handled
	// on the read loop precisely so its reply cannot race the `exit` behind it.
	s.send(`{"jsonrpc":"2.0","id":3,"method":"shutdown"}`)
	if got := string(s.read().ID); got != "3" {
		t.Fatalf("first answer id = %s, want 3 — shutdown waited behind the network", got)
	}

	sent := time.Now()
	s.send(`{"jsonrpc":"2.0","method":"exit"}`)

	// The lookup is still owed an answer. It gets one, and it is a RESULT with
	// a state — a network outcome is never a JSON-RPC error (DECISIONS.md 22).
	resp := s.read()

	// PROMPTLY. The answer arriving is only half of it: a lookup left on its own
	// five-second deadline would still be answered, but the window would sit
	// there not closing while a request nobody is waiting for runs down a clock.
	// Cancelling the session context is what makes this milliseconds; the bound
	// is generous only so a loaded CI box does not fail on scheduling noise.
	if waited := time.Since(sent); waited > 2*time.Second {
		t.Errorf("the outstanding lookup took %s to answer after exit — the session context is not reaching it", waited)
	}
	if got := string(resp.ID); got != "2" {
		t.Fatalf("the outstanding lookup answered with id %s, want 2", got)
	}
	if resp.Error != nil {
		t.Fatalf("a cancelled lookup came back as an error: %+v", resp.Error)
	}
	var r hub.Report
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.State != hub.StateCancelled {
		t.Errorf("state = %q (%s), want %q", r.State, r.Message, hub.StateCancelled)
	}
	if code := s.waitExit(); code != 0 {
		t.Errorf("exit after shutdown gave %d, want 0", code)
	}
}

// A handler now runs on its own goroutine, and a panic on a goroutine that
// nobody recovers takes the WHOLE PROCESS down — every other in-flight request
// with it. dispatch's recover cannot see it: it guards a different stack.
func TestAPanicInAConcurrentHandlerDoesNotKillTheLoop(t *testing.T) {
	h := newParkedHub(t)
	original := resolveFile
	resolveFile = func(string) (*resolve.Project, error) {
		panic("engineered panic inside resolve")
	}
	t.Cleanup(func() { resolveFile = original })

	path := writeFixture(t, "compose.yaml", "services:\n  web:\n    image: nginx:1.27\n")
	s := start(t)
	s.handshake(1)

	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{"ref":"postgres:16-alpine"}}`)
	h.waitEntered(t)

	// The panic happens while the lookup is outstanding, which is the case that
	// did not exist before this change.
	go s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"stack/resolve","params":{"path":%q}}`, path))
	resp := s.read()
	if got := string(resp.ID); got != "3" {
		t.Fatalf("first answer id = %s, want 3", got)
	}
	if resp.Error == nil || resp.Error.Code != codeInternal {
		t.Fatalf("a panic produced %+v, want an internal error", resp.Error)
	}

	// The session — and the outstanding lookup with it — survives.
	h.unblock()
	if got := string(s.read().ID); got != "2" {
		t.Fatal("the lookup outstanding across a panic was never answered")
	}
	resolveFile = original
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"stack/resolve","params":{"path":%q}}`, path))
	if next := s.read(); next.Error != nil {
		t.Fatalf("the session did not survive the panic: %+v", next.Error)
	}
}

// The write path is exclusive, and this is the stress that says so from the
// outside: eight saves and eight previews of ONE file, all in flight at once.
//
// edit.Apply reads the file, splices the buffer and writes the whole document
// back. Overlapping that with itself loses an update; overlapping it with a
// preview lets the preview parse a half-written file. Neither is a crash — this
// engine's failure mode is a confident wrong answer — so what is asserted is
// that every request succeeded and that the file still parses afterwards.
func TestConcurrentSavesAndPreviewsNeverSeeAHalfWrittenFile(t *testing.T) {
	path := writeFixture(t, "compose.yaml", "services:\n  web:\n    image: nginx:1.27\n")
	s := start(t)
	s.handshake(1)

	const n = 8
	go func() {
		for i := 0; i < n; i++ {
			s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"stack/apply","params":%s}`,
				100+i, mustJSON(map[string]any{
					"file": path,
					"ops": []map[string]any{{
						"operation": "replace_scalar",
						"at":        "services.web.image",
						"value":     fmt.Sprintf("nginx:1.%d", 30+i),
					}},
				})))
			s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"stack/preview","params":%s}`,
				200+i, mustJSON(map[string]any{
					"file": path,
					"ops": []map[string]any{{
						"operation": "replace_scalar",
						"at":        "services.web.image",
						"value":     "nginx:2.0",
					}},
				})))
		}
	}()

	for i := 0; i < 2*n; i++ {
		resp := s.read()
		if resp.Error != nil {
			t.Fatalf("a save or preview failed while others were in flight: %+v", resp.Error)
		}
	}

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	project, err := resolve.File(path)
	if err != nil {
		t.Fatalf("the file no longer parses after eight overlapping saves:\n%s\n%v", src, err)
	}
	if got := strings.Count(string(src), "image:"); got != 1 {
		t.Errorf("the file has %d image keys, want 1:\n%s", got, src)
	}
	if project == nil {
		t.Fatal("no project")
	}
}
