package sandbox_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/joyboydata/kit/hxchat/sandbox/demo"
	sandbox "github.com/joyboydata/kit/sandbox"
)

// TestServeWiring boots the real serve mount and asserts the page shape:
// one mount renders one chat-thread, ?two=1 renders two, seeds converge,
// and a full turn posts through the serve mount.
func TestServeWiring(t *testing.T) {
	store := demo.NewMemStore()
	mount, seeds := demo.Build(store)
	env, err := sandbox.Start(sandbox.Config{ //nolint:staticcheck // sanctioned Source use; whole-Config marker removal pending
		Source: sandbox.Static{
			Mount: mount,
			Seeds: seeds,
			Describe: func() any {
				return map[string]int{"t1": len(store.List("t1"))}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	client := env.WebClient()

	get := func(target string) (int, string) {
		resp, err := client.Get(env.BaseURL + target)
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

	if err := env.Seed("history"); err != nil {
		t.Fatal(err)
	}
	code, body := get("/chat/t1")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if got := strings.Count(body, "<chat-thread "); got != 1 {
		t.Fatalf("threads = %d, want 1", got)
	}
	if !strings.Contains(body, "hello back") {
		t.Fatal("history seed missing from page")
	}
	if !strings.Contains(body, `/hxchat/element.js`) {
		t.Fatal("head missing element include")
	}

	code, body = get("/chat/t1?two=1")
	if code != 200 {
		t.Fatalf("two code = %d", code)
	}
	if got := strings.Count(body, "<chat-thread "); got != 2 {
		t.Fatalf("threads = %d, want 2", got)
	}

	if err := env.Seed("empty"); err != nil {
		t.Fatal(err)
	}
	code, body = get("/chat/t1")
	if code != 200 {
		t.Fatalf("empty code = %d", code)
	}
	if strings.Contains(body, "hello back") {
		t.Fatal("empty seed leaks history")
	}

	form := url.Values{"text": {"hi"}}
	resp, err := client.PostForm(env.BaseURL+"/chat/t1/messages", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("plain post code = %d, want 204 hold", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		freq, err := http.NewRequest(http.MethodGet, env.BaseURL+"/chat/t1?view=thread", nil)
		if err != nil {
			t.Fatal(err)
		}
		freq.Header.Set("HX-Request", "true")
		fresp, err := client.Do(freq)
		if err != nil {
			t.Fatal(err)
		}
		fbody, err := io.ReadAll(fresp.Body)
		_ = fresp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(fbody), "echo: hi") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("serve mount never converged")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFailedSeed renders the error region with a retry affordance.
func TestFailedSeed(t *testing.T) {
	store := demo.NewMemStore()
	mount, seeds := demo.Build(store)
	env, err := sandbox.Start(sandbox.Config{ //nolint:staticcheck // sanctioned Source use; whole-Config marker removal pending
		Source: sandbox.Static{Mount: mount, Seeds: seeds},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if err := env.Seed("failed"); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, env.BaseURL+"/chat/t1?view=thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := env.WebClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`role="alert"`, "model blew up", `name="parent_run"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("failed seed missing %q:\n%s", want, body)
		}
	}
}
