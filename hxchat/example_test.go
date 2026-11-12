package hxchat_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/a-h/templ"

	"github.com/joyboydata/kit/chat"
	"github.com/joyboydata/kit/hxchat"
)

// stubStore is a minimal in-memory Store for the examples.
type stubStore struct {
	mu     sync.Mutex
	events map[string][]chat.Event
}

func newStubStore() *stubStore { return &stubStore{events: map[string][]chat.Event{}} }

func (s *stubStore) Append(threadID string, e chat.Event) (chat.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ThreadID = threadID
	s.events[threadID] = append(s.events[threadID], e)
	return e, nil
}

func (s *stubStore) List(threadID string) []chat.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]chat.Event(nil), s.events[threadID]...)
}

func (s *stubStore) Subscribe(threadID string) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

func ExampleNew() {
	h := hxchat.New(newStubStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
	for _, r := range h.Resource() {
		fmt.Println(r.Path)
	}
	// Output:
	// /chat/:thread
	// /hxchat/:file
}

func ExampleChat_Tag() {
	h := hxchat.New(newStubStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
	var b strings.Builder
	if err := h.Tag(hxchat.Mount{ThreadID: "abc"}).Render(context.Background(), &b); err != nil {
		panic(err)
	}
	fmt.Println(b.String()[:78])
	// Output:
	// <chat-thread thread="abc" prefix="abc" src="?view=thread" stream="abc/stream"
}

func ExampleChat_Head() {
	h := hxchat.New(newStubStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
	var b strings.Builder
	if err := h.Head().Render(context.Background(), &b); err != nil {
		panic(err)
	}
	fmt.Println(b.String())
	// Output:
	// <link rel="stylesheet" href="/hxchat/element.css"><script src="/hxchat/element.js" defer></script>
}

func ExampleChat_StreamURL() {
	h := hxchat.New(newStubStore(), runnerFunc(echoRunner), hxchat.Options{Path: "/chat/:thread"})
	fmt.Println(h.StreamURL(hxchat.Mount{ThreadID: "abc"}))
	// Output:
	// abc/stream
}

// ExampleDefaultMessageView_wrap shows the slot override model: decorate
// the Default, never reimplement it. Unset slots stay stock; a set slot
// wraps the stock renderer and the thread picks it up per message.
func ExampleDefaultMessageView_wrap() {
	decorated := func(m hxchat.Mount, msg chat.Message) templ.Component {
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
	}
	var b strings.Builder
	msg := chat.Message{ID: "m1", Role: "user", Text: "hi"}
	if err := decorated(hxchat.Mount{ThreadID: "abc"}, msg).Render(context.Background(), &b); err != nil {
		panic(err)
	}
	fmt.Println(b.String())
	// Output:
	// <section class="wrap"><div class="hxchat-msg" data-role="user" data-message-id="m1"><p>hi</p></div></section>
}
