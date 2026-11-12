package hxchat_test

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"

	"github.com/joyboydata/kit/agui"
	"github.com/joyboydata/kit/hxchat"
	v2 "github.com/joyboydata/kit/hxkit/v2"
)

var update = flag.Bool("update", false, "rewrite golden files")

// memStore is the Stub store: per-thread logs plus broadcast. Append
// assigns Seq and signals subscribers non-blockingly.
type memStore struct {
	mu     sync.Mutex
	events map[string][]agui.Event
	subs   map[string]map[chan struct{}]struct{}
}

func newMemStore() *memStore {
	return &memStore{events: map[string][]agui.Event{}, subs: map[string]map[chan struct{}]struct{}{}}
}

func (s *memStore) Append(threadID string, e agui.Event) (agui.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ThreadID = threadID
	e.Seq = int64(len(s.events[threadID])) + 1
	s.events[threadID] = append(s.events[threadID], e)
	for ch := range s.subs[threadID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return e, nil
}

func (s *memStore) List(threadID string) []agui.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agui.Event(nil), s.events[threadID]...)
}

func (s *memStore) Subscribe(threadID string) (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 8)
	if s.subs[threadID] == nil {
		s.subs[threadID] = map[chan struct{}]struct{}{}
	}
	s.subs[threadID][ch] = struct{}{}
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs[threadID], ch)
	}
}

// runnerFunc adapts a func to the Runner seam.
type runnerFunc func(ctx context.Context, in agui.RunInput, em *agui.Emitter) error

func (f runnerFunc) Run(ctx context.Context, in agui.RunInput, em *agui.Emitter) error {
	return f(ctx, in, em)
}

// echoRunner answers with one text turn naming the input.
func echoRunner(_ context.Context, in agui.RunInput, em *agui.Emitter) error {
	mid := "m-" + in.RunID
	if err := em.StartText(mid, "assistant"); err != nil {
		return err
	}
	if err := em.WriteText(mid, "echo: "+in.Message.Text); err != nil {
		return err
	}
	if err := em.EndText(mid); err != nil {
		return err
	}
	return em.FinishRun(nil, "stub")
}

// failRunner closes the run as failed.
func failRunner(_ context.Context, in agui.RunInput, em *agui.Emitter) error {
	return em.FailRun("model blew up", "E_MODEL")
}

// renderHTML renders a component to a string.
func renderHTML(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// fixtureMount is the golden mount: fixed prefix, no theme.
func fixtureMount() hxchat.Mount {
	return hxchat.Mount{ThreadID: "abc", Prefix: "abc"}
}

// fixtureMessages covers text, escaping, and a tool call with outcome.
func fixtureMessages() []agui.Message {
	return []agui.Message{
		{ID: "m1", Role: "user", Text: "Hello <world>"},
		{ID: "m2", Role: "assistant", Text: "Hi there",
			ToolCalls: []agui.ToolCall{{ID: "t1", Name: "search", Args: `{"q":"x"}`, Result: "ok"}}},
	}
}

// golden compares a render against testdata/<name>.golden.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch:\n got: %q\nwant: %q", name, got, want)
	}
}

func TestFragmentGoldens(t *testing.T) {
	m := fixtureMount()
	msgs := fixtureMessages()
	golden(t, "thread", renderHTML(t, hxchat.DefaultThreadView(m, msgs)))
	golden(t, "message", renderHTML(t, hxchat.DefaultMessageView(m, msgs[1])))
	golden(t, "composer", renderHTML(t, hxchat.DefaultComposerView(m, false)))
	golden(t, "composer_locked", renderHTML(t, hxchat.DefaultComposerView(m, true)))
	golden(t, "status_running", renderHTML(t, hxchat.DefaultStatusView(m, hxchat.RunState{Phase: hxchat.PhaseRunning, RunID: "r7"})))
	golden(t, "status_idle", renderHTML(t, hxchat.DefaultStatusView(m, hxchat.RunState{Phase: hxchat.PhaseIdle})))
	golden(t, "error", renderHTML(t, hxchat.DefaultErrorView(m, fmt.Errorf("model blew up (E_MODEL)"), "r9")))
}

// serve wires a test echo with the widget resource only (nil layout, so
// asset bytes serve verbatim) plus a page echo URL for Tag resolution.
func serve(t *testing.T, h *hxchat.Chat) *httptest.Server {
	t.Helper()
	e := echo.New()
	v2.App{Resources: h.Resource()}.Register(e)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string, htmx bool) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func postForm(t *testing.T, srv *httptest.Server, path string, form url.Values, htmx bool) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("body missing %q:\n%s", want, body)
	}
}

func TestServeContract(t *testing.T) {
	h := hxchat.New(newMemStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
	srv := serve(t, h)

	t.Run("thread fragment is embed-only HTML", func(t *testing.T) {
		code, hdr, body := get(t, srv, "/chat/t1?view=thread", true)
		if code != 200 {
			t.Fatalf("code = %d", code)
		}
		if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("content-type = %q", ct)
		}
		mustContain(t, body, `id="t1-messages"`)
		mustContain(t, body, `id="t1-composer"`)
		mustContain(t, body, `id="t1-status"`)
		mustContain(t, body, `id="t1-error"`)
	})

	t.Run("thread direct visit 404s", func(t *testing.T) {
		code, _, _ := get(t, srv, "/chat/t1?view=thread", false)
		if code != 404 {
			t.Fatalf("code = %d", code)
		}
	})

	t.Run("unknown view 404s", func(t *testing.T) {
		code, _, _ := get(t, srv, "/chat/t1?view=nope", true)
		if code != 404 {
			t.Fatalf("code = %d", code)
		}
	})

	t.Run("element.js serves immutable javascript", func(t *testing.T) {
		code, hdr, body := get(t, srv, "/hxchat/element.js", false)
		if code != 200 {
			t.Fatalf("code = %d", code)
		}
		if ct := hdr.Get("Content-Type"); ct != "application/javascript" {
			t.Fatalf("content-type = %q", ct)
		}
		if cc := hdr.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Fatalf("cache-control = %q", cc)
		}
		mustContain(t, body, `customElements.define("chat-thread"`)
	})

	t.Run("element.css serves immutable css", func(t *testing.T) {
		code, hdr, body := get(t, srv, "/hxchat/element.css", false)
		if code != 200 {
			t.Fatalf("code = %d", code)
		}
		if ct := hdr.Get("Content-Type"); ct != "text/css" {
			t.Fatalf("content-type = %q", ct)
		}
		if cc := hdr.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Fatalf("cache-control = %q", cc)
		}
		mustContain(t, body, `chat-thread`)
	})

	t.Run("unknown asset 404s", func(t *testing.T) {
		code, _, _ := get(t, srv, "/hxchat/other.js", false)
		if code != 404 {
			t.Fatalf("code = %d", code)
		}
	})

	t.Run("tag URLs resolve on the resource", func(t *testing.T) {
		tag := renderHTML(t, h.Tag(hxchat.Mount{ThreadID: "t1"}))
		mustContain(t, tag, `<chat-thread thread="t1" prefix="t1"`)
		mustContain(t, tag, `src="?view=thread" stream="t1/stream" post="t1/messages"`)
		base, _ := url.Parse(srv.URL + "/chat/t1")
		for _, attr := range []string{`src="`, `stream="`, `post="`} {
			i := strings.Index(tag, attr)
			rel := tag[i+len(attr):]
			rel = rel[:strings.Index(rel, `"`)]
			u := base.ResolveReference(mustParseURL(t, rel))
			if u.Host != base.Host {
				t.Fatalf("tag URL escapes host: %q", rel)
			}
			if u.Path == "/chat/t1/stream" {
				continue // SSE shape covered by TestStreams
			}
			if u.Path == "/chat/t1/messages" {
				code, _ := postForm(t, srv, u.RequestURI(), url.Values{"text": {"hi"}}, true)
				if code != 200 {
					t.Fatalf("POST %s = %d", u.RequestURI(), code)
				}
				continue
			}
			code, _, _ := get(t, srv, u.RequestURI(), true)
			if code != 200 {
				t.Fatalf("GET %s = %d", u.RequestURI(), code)
			}
		}
	})

	t.Run("head emits asset includes", func(t *testing.T) {
		head := renderHTML(t, h.Head())
		mustContain(t, head, `<link rel="stylesheet" href="/hxchat/element.css">`)
		mustContain(t, head, `<script src="/hxchat/element.js" defer></script>`)
	})

	t.Run("stream URL matches tag", func(t *testing.T) {
		if got := h.StreamURL(hxchat.Mount{ThreadID: "t1"}); got != "t1/stream" {
			t.Fatalf("StreamURL = %q", got)
		}
	})
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestMutations(t *testing.T) {
	t.Run("post returns hold for both callers", func(t *testing.T) {
		for _, htmx := range []bool{true, false} {
			h := hxchat.New(newMemStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
			srv := serve(t, h)
			code, body := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"hi"}}, htmx)
			want := 200
			if !htmx {
				want = 204
			}
			if code != want {
				t.Fatalf("htmx=%v code = %d, want %d", htmx, code, want)
			}
			if body != "" {
				t.Fatalf("htmx=%v body = %q, want empty hold", htmx, body)
			}
		}
	})

	t.Run("empty text is a 400 naming the field", func(t *testing.T) {
		h := hxchat.New(newMemStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
		srv := serve(t, h)
		code, _ := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"  "}}, true)
		if code != 400 {
			t.Fatalf("code = %d, want 400", code)
		}
	})

	t.Run("run locks then streams to unlock", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		blocking := runnerFunc(func(_ context.Context, in agui.RunInput, em *agui.Emitter) error {
			once.Do(func() { close(started) })
			<-release
			return echoRunner(context.Background(), in, em)
		})
		h := hxchat.New(newMemStore(), blocking, hxchat.Options{Path: "/chat/:thread"})
		srv := serve(t, h)

		code, _ := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"hi"}}, true)
		if code != 200 {
			t.Fatalf("code = %d", code)
		}
		<-started
		_, _, locked := get(t, srv, "/chat/t1?view=thread", true)
		mustContain(t, locked, `disabled`)
		mustContain(t, locked, `Working...`)
		close(release)
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, _, body := get(t, srv, "/chat/t1?view=thread", true)
			if strings.Contains(body, "echo: hi") && !strings.Contains(body, "disabled") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("run never unlocked:\n%s", body)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("failed run renders error with retry", func(t *testing.T) {
		h := hxchat.New(newMemStore(), runnerFunc(failRunner), hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		code, _ := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"hi"}}, true)
		if code != 200 {
			t.Fatalf("code = %d", code)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, _, body := get(t, srv, "/chat/t1?view=thread", true)
			if strings.Contains(body, `role="alert"`) && strings.Contains(body, `model blew up (E_MODEL)`) {
				mustContain(t, body, `name="parent_run"`)
				mustContain(t, body, `>Retry<`)
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("error never rendered:\n%s", body)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// sseFrame is one parsed SSE frame.
type sseFrame struct {
	event string
	data  string
}

// openSSE opens the stream and collects frames until ctx ends.
func openSSE(t *testing.T, srv *httptest.Server, target string, out chan<- sseFrame, ctx context.Context) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var ev string
		var lines []string
		flush := func() {
			if ev != "" {
				out <- sseFrame{event: ev, data: strings.Join(lines, "\n")}
			}
			ev = ""
			lines = nil
		}
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				flush()
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				ev = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				lines = append(lines, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
}

// collectFrames gathers frames until want matches or timeout hits.
func collectFrames(t *testing.T, ch <-chan sseFrame, timeout time.Duration, want func(sseFrame) bool) sseFrame {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case f := <-ch:
			if want(f) {
				return f
			}
		case <-deadline:
			t.Fatal("timed out waiting for frame")
			return sseFrame{}
		}
	}
}

// seedHistory writes one finished user/assistant turn into the store.
func seedHistory(t *testing.T, s *memStore, thread string) {
	t.Helper()
	l := agui.NewLog(thread)
	em := agui.NewEmitter(l)
	in := agui.RunInput{ThreadID: thread, RunID: "r0", Message: agui.Message{ID: "m0", Role: "user", Text: "hey"}}
	if err := em.StartRun(in); err != nil {
		t.Fatal(err)
	}
	if err := em.StartText("m0", "user"); err != nil {
		t.Fatal(err)
	}
	if err := em.WriteText("m0", "hey"); err != nil {
		t.Fatal(err)
	}
	if err := em.EndText("m0"); err != nil {
		t.Fatal(err)
	}
	if err := em.StartText("m1", "assistant"); err != nil {
		t.Fatal(err)
	}
	if err := em.WriteText("m1", "hello back"); err != nil {
		t.Fatal(err)
	}
	if err := em.EndText("m1"); err != nil {
		t.Fatal(err)
	}
	if err := em.FinishRun(nil, "stub"); err != nil {
		t.Fatal(err)
	}
	for _, e := range l.List() {
		if _, err := s.Append(thread, e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStreams(t *testing.T) {
	t.Run("replay converges on connect", func(t *testing.T) {
		s := newMemStore()
		seedHistory(t, s, "t1")
		h := hxchat.New(s, runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames := make(chan sseFrame, 16)
		openSSE(t, srv, "/chat/t1/stream?hxchatns=n1", frames, ctx)
		got := collectFrames(t, frames, 5*time.Second, func(f sseFrame) bool {
			return f.event == "t1-n1-messages"
		})
		mustContain(t, got.data, "hello back")
	})

	t.Run("emit pushes new content", func(t *testing.T) {
		release := make(chan struct{})
		var once sync.Once
		blocking := runnerFunc(func(_ context.Context, in agui.RunInput, em *agui.Emitter) error {
			<-release
			return echoRunner(context.Background(), in, em)
		})
		h := hxchat.New(newMemStore(), blocking, hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		if code, _ := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"hi"}}, true); code != 200 {
			t.Fatalf("code = %d", code)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames := make(chan sseFrame, 32)
		openSSE(t, srv, "/chat/t1/stream?hxchatns=n1", frames, ctx)
		// Replay shows the open run: locked composer, running status.
		composer := collectFrames(t, frames, 5*time.Second, func(f sseFrame) bool {
			return f.event == "t1-n1-composer"
		})
		mustContain(t, composer.data, "disabled")
		once.Do(func() { close(release) })
		done := collectFrames(t, frames, 5*time.Second, func(f sseFrame) bool {
			return f.event == "t1-n1-messages" && strings.Contains(f.data, "echo: hi")
		})
		mustContain(t, done.data, "echo: hi")
		collectFrames(t, frames, 5*time.Second, func(f sseFrame) bool { return f.event == "redirect" })
	})

	t.Run("dedup suppresses identical frames", func(t *testing.T) {
		release := make(chan struct{})
		blocking := runnerFunc(func(_ context.Context, in agui.RunInput, em *agui.Emitter) error {
			<-release
			return echoRunner(context.Background(), in, em)
		})
		s := newMemStore()
		h := hxchat.New(s, blocking, hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		if code, _ := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"hi"}}, true); code != 200 {
			t.Fatalf("code = %d", code)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		defer close(release) // run stays open for the whole row
		frames := make(chan sseFrame, 64)
		openSSE(t, srv, "/chat/t1/stream?hxchatns=n1", frames, ctx)
		// Drain the replay: one frame per region.
		for _, want := range []string{"t1-n1-messages", "t1-n1-composer", "t1-n1-status", "t1-n1-error"} {
			collectFrames(t, frames, 5*time.Second, func(f sseFrame) bool { return f.event == want })
		}
		// A notify with no visible change (step frames fold to nothing)
		// must not produce a duplicate frame.
		if _, err := s.Append("t1", agui.Event{Type: agui.EventStepStarted, RunID: "r-nope", StepName: "s1"}); err != nil {
			t.Fatal(err)
		}
		select {
		case f := <-frames:
			t.Fatalf("duplicate frame streamed: %v", f)
		case <-time.After(400 * time.Millisecond):
		}
	})

	t.Run("terminal closes after the run", func(t *testing.T) {
		h := hxchat.New(newMemStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		if code, _ := postForm(t, srv, "/chat/t1/messages", url.Values{"text": {"hi"}}, true); code != 200 {
			t.Fatalf("code = %d", code)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		frames := make(chan sseFrame, 32)
		closed := make(chan struct{})
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/chat/t1/stream?hxchatns=n1", nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			sc := bufio.NewScanner(resp.Body)
			var ev string
			for sc.Scan() {
				line := sc.Text()
				if strings.HasPrefix(line, "event: ") {
					ev = strings.TrimPrefix(line, "event: ")
				} else if line == "" && ev != "" {
					frames <- sseFrame{event: ev}
					ev = ""
				}
			}
			close(closed)
		}()
		collectFrames(t, frames, 7*time.Second, func(f sseFrame) bool { return f.event == "redirect" })
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("stream did not close after terminal")
		}
	})

	t.Run("connection prefix overrides the thread default", func(t *testing.T) {
		s := newMemStore()
		seedHistory(t, s, "t1")
		h := hxchat.New(s, runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames := make(chan sseFrame, 16)
		openSSE(t, srv, "/chat/t1/stream?prefix=t1-b&hxchatns=n1", frames, ctx)
		got := collectFrames(t, frames, 5*time.Second, func(f sseFrame) bool {
			return strings.HasSuffix(f.event, "-messages")
		})
		if got.event != "t1-b-n1-messages" {
			t.Fatalf("frame name = %q, want t1-b-n1-messages", got.event)
		}
	})

	t.Run("ns isolates frame names", func(t *testing.T) {
		s := newMemStore()
		seedHistory(t, s, "t1")
		h := hxchat.New(s, runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread", Tick: 10 * time.Millisecond})
		srv := serve(t, h)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := make(chan sseFrame, 16)
		b := make(chan sseFrame, 16)
		openSSE(t, srv, "/chat/t1/stream?hxchatns=a", a, ctx)
		openSSE(t, srv, "/chat/t1/stream?hxchatns=b", b, ctx)
		fa := collectFrames(t, a, 5*time.Second, func(f sseFrame) bool { return strings.HasSuffix(f.event, "-messages") })
		fb := collectFrames(t, b, 5*time.Second, func(f sseFrame) bool { return strings.HasSuffix(f.event, "-messages") })
		if fa.event != "t1-a-messages" || fb.event != "t1-b-messages" {
			t.Fatalf("frame names = %q, %q", fa.event, fb.event)
		}
	})
}

func TestSlotOverride(t *testing.T) {
	sCustom, sStock := newMemStore(), newMemStore()
	seedHistory(t, sCustom, "abc")
	seedHistory(t, sStock, "abc")
	decorated := hxchat.New(sCustom, runnerFunc(echoRunner), hxchat.Options{
		Path: "/chat/:thread",
		RenderMessage: func(m hxchat.Mount, msg agui.Message) templ.Component {
			inner := hxchat.DefaultMessageView(m, msg)
			return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
				if _, err := io.WriteString(w, `<section class="wrap">`); err != nil {
					return err
				}
				if err := inner.Render(ctx, w); err != nil {
					return err
				}
				_, err := io.WriteString(w, `</section>`)
				return err
			})
		},
	})
	stock := hxchat.New(sStock, runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})

	m := fixtureMount()
	// Thread render goes through the override: every message wrapped.
	threadCustom := renderThreadOf(t, decorated, m)
	threadStock := renderThreadOf(t, stock, m)
	mustContain(t, threadCustom, `<section class="wrap">`)
	if strings.Contains(threadStock, `<section class="wrap">`) {
		t.Fatal("stock thread contains the decorator")
	}
	// Stock regions unchanged: removing the wrapper tags must recover
	// the stock thread byte-for-byte (empty store, so regions are empty
	// shells and the composer/status/error slots are stock).
	unwrapped := strings.ReplaceAll(threadCustom, `<section class="wrap">`, "")
	unwrapped = strings.ReplaceAll(unwrapped, `</section>`, "")
	if strings.Contains(threadStock, `<section`) {
		t.Fatal("stock thread contains section markup")
	}
	if unwrapped != threadStock {
		t.Fatalf("non-message regions diverged:\n%s\n---\n%s", unwrapped, threadStock)
	}
}

// renderThreadOf renders the thread fragment for a chat via ?view=thread.
func renderThreadOf(t *testing.T, h *hxchat.Chat, m hxchat.Mount) string {
	t.Helper()
	e := echo.New()
	v2.App{Resources: h.Resource()}.Register(e)
	req := httptest.NewRequest(http.MethodGet, "/chat/"+m.ThreadID+"?view=thread", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	return rec.Body.String()
}
