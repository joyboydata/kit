package agui

// ADK adapter tests: the fold pins are ported from the apliq projection
// suite (same feed order, same assertions, kit names); the event/terminal
// pins cover the kit-only layer (triads validate, terminal enriches).

import (
	"context"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func adkTextEvent(ctx context.Context, runID, role, text string) *session.Event {
	ev := session.NewEvent(ctx, runID)
	ev.Content = &genai.Content{Role: role, Parts: []*genai.Part{{Text: text}}}
	return ev
}

func TestFoldADKRoles(t *testing.T) {
	ctx := context.Background()
	evs := []*session.Event{
		adkTextEvent(ctx, "r1", "user", "hello"),
		adkTextEvent(ctx, "r1", "model", "hi there"),
	}
	got := FoldADK(evs)
	if len(got) != 2 {
		t.Fatalf("folded = %d, want 2", len(got))
	}
	if got[0].Role != "user" || got[0].Text != "hello" || got[0].RunID != "r1" {
		t.Fatalf("first = %+v, want user/hello/r1", got[0])
	}
	if got[1].Role != "assistant" || got[1].Text != "hi there" {
		t.Fatalf("second = %+v, want assistant/hi there", got[1])
	}
}

func TestFoldADKSkips(t *testing.T) {
	ctx := context.Background()
	empty := session.NewEvent(ctx, "r2") // no content: state-only
	tool := adkTextEvent(ctx, "r2", "tool", "result blob")
	user := adkTextEvent(ctx, "r2", "user", "kept")
	got := FoldADK([]*session.Event{empty, tool, user})
	if len(got) != 1 || got[0].Text != "kept" || got[0].Role != "user" {
		t.Fatalf("folded = %+v, want only the user message", got)
	}
}

func TestFoldADKEmpty(t *testing.T) {
	if got := FoldADK(nil); len(got) != 0 {
		t.Fatalf("folded nil = %+v, want empty", got)
	}
}

func adkModelEvent(ctx context.Context, inv, author, text string, at int64) *session.Event {
	ev := session.NewEvent(ctx, inv)
	ev.Author = author
	ev.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text}}}
	ev.Timestamp = time.UnixMilli(at)
	return ev
}

func TestFoldADKHopMergesSameAuthor(t *testing.T) {
	ctx := context.Background()
	// Streamed chunks from one author fold into one hop (rows: one bubble).
	evs := []*session.Event{
		adkModelEvent(ctx, "inv-1", "writer", "hel", 1000),
		adkModelEvent(ctx, "inv-1", "writer", "lo", 1001),
	}
	got := FoldADK(evs)
	if len(got) != 1 {
		t.Fatalf("folded = %d, want 1 merged hop", len(got))
	}
	if len(got[0].Blocks) != 2 || got[0].RunID != "inv-1" {
		t.Fatalf("hop = %+v, want 2 blocks run inv-1", got[0])
	}
}

func TestFoldADKToolCallAndMarker(t *testing.T) {
	ctx := context.Background()
	call := session.NewEvent(ctx, "inv-9")
	call.Author = "writer"
	call.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{
		FunctionCall: &genai.FunctionCall{ID: "c1", Name: "write_file", Args: map[string]any{"path": "a.txt"}},
	}}}
	call.Timestamp = time.UnixMilli(2000)
	resp := session.NewEvent(ctx, "inv-9")
	resp.Author = "writer"
	resp.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{ID: "c1", Response: map[string]any{"ok": true}},
	}}}
	resp.Timestamp = time.UnixMilli(2001)
	marker := session.NewEvent(ctx, "inv-9")
	marker.Author = "writer"
	marker.Timestamp = time.UnixMilli(2002)
	marker.Actions.StateDelta = map[string]any{
		"turn.t1.outcome":    TerminalSuccess,
		"turn.t1.runId":      "inv-9",
		"turn.t1.model":      "stub-model",
		"turn.t1.stopReason": "stop",
	}
	got := FoldADK([]*session.Event{call, resp, marker})
	if len(got) != 1 {
		t.Fatalf("folded = %d, want 1 tool hop", len(got))
	}
	hop := got[0]
	if hop.Role != "assistant" || len(hop.Blocks) != 1 || hop.Blocks[0].Call == nil {
		t.Fatalf("hop = %+v, want 1 tool block", hop)
	}
	if hop.Blocks[0].Call.Name != "write_file" {
		t.Fatalf("call = %+v, want write_file", hop.Blocks[0].Call)
	}
	if hop.Model != "stub-model" || hop.StopReason != "stop" {
		t.Fatalf("enrichment = %q/%q, want stub-model/stop", hop.Model, hop.StopReason)
	}
}

func TestProjectADKEvent(t *testing.T) {
	ctx := context.Background()
	// Content event projects text + run.
	text, runID, ok := ProjectADKEvent(adkTextEvent(ctx, "r1", "user", "hi"))
	if !ok || text != "hi" || runID != "r1" {
		t.Fatalf("project = %q/%q/%v, want hi/r1/true", text, runID, ok)
	}
	// State-only event projects nothing.
	if _, _, ok := ProjectADKEvent(session.NewEvent(ctx, "r1")); ok {
		t.Fatal("state-only projects ok=true, want false")
	}
	// Thought text is reasoning, never transcript text.
	ev := session.NewEvent(ctx, "r1")
	ev.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "thinking", Thought: true}}}
	if _, _, ok := ProjectADKEvent(ev); ok {
		t.Fatal("thought-only projects ok=true, want false")
	}
}

func TestFoldADKThought(t *testing.T) {
	ctx := context.Background()
	ev := session.NewEvent(ctx, "inv-3")
	ev.Author = "writer"
	ev.Content = &genai.Content{Role: "model", Parts: []*genai.Part{
		{Text: "let me think", Thought: true},
		{Text: "the answer"},
	}}
	ev.Timestamp = time.UnixMilli(3000)
	got := FoldADK([]*session.Event{ev})
	if len(got) != 1 {
		t.Fatalf("folded = %d, want 1 hop", len(got))
	}
	if got[0].Text != "the answer" {
		t.Fatalf("text = %q, want only the non-thought part", got[0].Text)
	}
	if len(got[0].Blocks) != 2 || got[0].Blocks[0].Thought != "let me think" {
		t.Fatalf("blocks = %+v, want thought first", got[0].Blocks)
	}
}

func TestFoldADKInterrupt(t *testing.T) {
	ctx := context.Background()
	ev := session.NewEvent(ctx, "inv-4")
	ev.Timestamp = time.UnixMilli(4000)
	ev.RequestedInput = &session.RequestInput{InterruptID: "in-1", Message: "approve?"}
	got := FoldADK([]*session.Event{ev})
	if len(got) != 1 || got[0].Role != "activity" {
		t.Fatalf("folded = %+v, want 1 activity message", got)
	}
	if got[0].ActivityType != "interrupt" {
		t.Fatalf("activity = %+v, want interrupt type", got[0])
	}
	// Interrupt on a content event folds alongside the text.
	withText := adkTextEvent(ctx, "inv-4", "user", "go")
	withText.RequestedInput = &session.RequestInput{InterruptID: "in-2"}
	got = FoldADK([]*session.Event{withText})
	if len(got) != 2 || got[1].Role != "activity" {
		t.Fatalf("folded = %+v, want text + activity", got)
	}
}

func TestADKEventsRoundTrip(t *testing.T) {
	ctx := context.Background()
	call := session.NewEvent(ctx, "inv-9")
	call.Author = "writer"
	call.Content = &genai.Content{Role: "model", Parts: []*genai.Part{
		{Text: "working", Thought: true},
		{Text: "done"},
		{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "write_file", Args: map[string]any{"path": "a.txt"}}},
	}}
	call.Timestamp = time.UnixMilli(2000)
	resp := session.NewEvent(ctx, "inv-9")
	resp.Author = "writer"
	resp.Content = &genai.Content{Role: "model", Parts: []*genai.Part{
		{FunctionResponse: &genai.FunctionResponse{ID: "c1", Response: map[string]any{"output": "ok"}}},
	}}
	resp.Timestamp = time.UnixMilli(2001)
	user := adkTextEvent(ctx, "inv-9", "user", "write it")
	user.Timestamp = time.UnixMilli(1999)
	evs := []*session.Event{user, call, resp}

	events := ADKEvents("th-1", "run-1", FoldADK(evs))
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v invalid: %v", e, err)
		}
	}
	l := NewLog("th-1")
	// Folded hops keep their ADK invocation as RunID (rows: one run per
	// hop); the caller opens that run before appending the triads.
	if _, err := l.Append(Event{Type: EventRunStarted, ThreadID: "th-1", RunID: "inv-9"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, e := range events {
		if _, err := l.Append(e); err != nil {
			t.Fatalf("append %+v: %v", e, err)
		}
	}
	msgs := l.Fold()
	if len(msgs) != 3 { // user text + assistant text + reasoning
		t.Fatalf("folded = %+v, want 3 messages", msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "write it" {
		t.Fatalf("first = %+v, want user text", msgs[0])
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "write_file" {
		t.Fatalf("second = %+v, want tool call", msgs[1])
	}
	if msgs[1].ToolCalls[0].Result != `{"output":"ok"}` {
		t.Fatalf("result = %v, want compact JSON", msgs[1].ToolCalls[0].Result)
	}
	if msgs[2].Role != "reasoning" || msgs[2].Text != "working" {
		t.Fatalf("third = %+v, want reasoning", msgs[2])
	}
}

func TestADKRunEnd(t *testing.T) {
	ctx := context.Background()
	model := adkModelEvent(ctx, "inv-5", "writer", "hi", 5000)
	model.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 10, CandidatesTokenCount: 4, TotalTokenCount: 14,
	}
	marker := session.NewEvent(ctx, "inv-5")
	marker.Timestamp = time.UnixMilli(5001)
	marker.Actions.StateDelta = map[string]any{
		"turn.t1.outcome": TerminalSuccess,
		"turn.t1.runId":   "inv-5",
		"turn.t1.model":   "stub-model",
	}
	end, ok := ADKRunEnd([]*session.Event{model, marker})
	if !ok {
		t.Fatal("no terminal, want success")
	}
	if end.Model != "stub-model" {
		t.Fatalf("model = %q, want stub-model", end.Model)
	}
	if end.Usage == nil || end.Usage.Input != 10 || end.Usage.Output != 4 || end.Usage.Total != 14 {
		t.Fatalf("usage = %+v, want 10/4/14", end.Usage)
	}
	if _, ok := ADKRunEnd([]*session.Event{model}); ok {
		t.Fatal("marker-less run ends ok=true, want false")
	}
}

func TestADKRunError(t *testing.T) {
	ctx := context.Background()
	bad := session.NewEvent(ctx, "inv-6")
	bad.Timestamp = time.UnixMilli(6000)
	bad.ErrorCode = "MODEL_DOWN"
	bad.ErrorMessage = "model blew up"
	marker := session.NewEvent(ctx, "inv-6")
	marker.Timestamp = time.UnixMilli(6001)
	marker.Actions.StateDelta = map[string]any{"turn.t1.outcome": TerminalError}
	msg, code, ok := ADKRunError([]*session.Event{bad, marker})
	if !ok || msg != "model blew up" || code != "MODEL_DOWN" {
		t.Fatalf("error = %q/%q/%v, want model blew up/MODEL_DOWN/true", msg, code, ok)
	}
	if _, _, ok := ADKRunError([]*session.Event{bad}); ok {
		t.Fatal("marker-less run errors ok=true, want false")
	}
}
