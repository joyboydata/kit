package chat

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// openRun returns a Log with run-1 started.
func openRun(t *testing.T) *Log {
	t.Helper()
	l := NewLog("thread-1")
	_, err := l.Append(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-1"})
	require.NoError(t, err)
	return l
}

func TestTriadDiscipline(t *testing.T) {
	text := func(l *Log) error {
		_, err := l.Append(Event{Type: EventTextMessageContent, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Delta: "x"})
		return err
	}
	args := func(l *Log) error {
		_, err := l.Append(Event{Type: EventToolCallArgs, ThreadID: "thread-1", RunID: "run-1", ToolCallID: "t-1", Delta: "{}"})
		return err
	}
	reason := func(l *Log) error {
		_, err := l.Append(Event{Type: EventReasoningMessageContent, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-9", Delta: "x"})
		return err
	}

	t.Run("content before START", func(t *testing.T) {
		require.Error(t, text(openRun(t)))
		require.Error(t, args(openRun(t)))
	})
	t.Run("reasoning content without block", func(t *testing.T) {
		require.Error(t, reason(openRun(t)))
	})
	t.Run("content without run", func(t *testing.T) {
		l := NewLog("thread-1") // RUN_STARTED never appended
		require.Error(t, text(l))
	})
	t.Run("content after END", func(t *testing.T) {
		l := openRun(t)
		_, err := l.Append(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant"})
		require.NoError(t, err)
		_, err = l.Append(Event{Type: EventTextMessageEnd, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1"})
		require.NoError(t, err)
		require.Error(t, text(l), "content after END")
	})
	t.Run("args after END", func(t *testing.T) {
		l := openRun(t)
		mk := func(typ EventType) Event {
			return Event{Type: typ, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", ToolCallID: "t-1", Delta: "n"}
		}
		_, err := l.Append(mk(EventToolCallStart))
		require.NoError(t, err)
		_, err = l.Append(Event{Type: EventToolCallEnd, ThreadID: "thread-1", RunID: "run-1", ToolCallID: "t-1"})
		require.NoError(t, err)
		require.Error(t, args(l))
	})
	t.Run("double START and END without START", func(t *testing.T) {
		l := openRun(t)
		start := Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant"}
		_, err := l.Append(start)
		require.NoError(t, err)
		// Identical retry is idempotent; a conflicting reopen fails.
		dup, err := l.Append(start)
		require.NoError(t, err)
		require.Equal(t, int64(2), dup.Seq)
		conflict := start
		conflict.Role = "user"
		_, err = l.Append(conflict)
		require.Error(t, err, "conflicting START")
		_, err = l.Append(Event{Type: EventTextMessageEnd, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-2"})
		require.Error(t, err, "END without START")
	})
	t.Run("reasoning END with open message", func(t *testing.T) {
		l := openRun(t)
		for _, typ := range []EventType{EventReasoningStart, EventReasoningMessageStart} {
			_, err := l.Append(Event{Type: typ, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-9"})
			require.NoError(t, err)
		}
		_, err := l.Append(Event{Type: EventReasoningEnd, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-9"})
		require.Error(t, err, "block must not close under an open message")
	})
}

func TestRunLifecycle(t *testing.T) {
	t.Run("terminal without open run", func(t *testing.T) {
		l := NewLog("thread-1")
		_, err := l.Append(Event{Type: EventRunFinished, ThreadID: "thread-1", RunID: "ghost"})
		require.Error(t, err)
		_, err = l.Append(Event{Type: EventRunError, ThreadID: "thread-1", RunID: "ghost", Message: "boom"})
		require.Error(t, err)
	})
	t.Run("double terminal", func(t *testing.T) {
		l := openRun(t)
		_, err := l.Append(Event{Type: EventRunFinished, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
		// A *different* terminal for the same run must fail ...
		_, err = l.Append(Event{Type: EventRunError, ThreadID: "thread-1", RunID: "run-1", Message: "boom", Code: "E"})
		require.Error(t, err, "second terminal must fail")
		// ... while retrying the identical terminal is idempotent.
		dup, err := l.Append(Event{Type: EventRunFinished, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
		require.Equal(t, int64(2), dup.Seq)
	})
	t.Run("events after terminal", func(t *testing.T) {
		l := openRun(t)
		_, err := l.Append(Event{Type: EventRunFinished, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
		_, err = l.Append(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant"})
		require.Error(t, err)
	})
	t.Run("duplicate run start", func(t *testing.T) {
		l := openRun(t)
		// Identical retry is idempotent; a changed reopen is rejected.
		dup, err := l.Append(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
		require.Equal(t, int64(1), dup.Seq)
		_, err = l.Append(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-1", ParentRunID: "run-0"})
		require.Error(t, err)
	})
	t.Run("thread mismatch", func(t *testing.T) {
		l := openRun(t)
		_, err := l.Append(Event{Type: EventRunStarted, ThreadID: "other", RunID: "run-2"})
		require.Error(t, err)
	})
}

func TestAbsentMeansAbsent(t *testing.T) {
	e := Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant"}
	raw, err := Encode(e)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "null", "omitted fields must be absent, never null")
	back, err := Decode(raw)
	require.NoError(t, err)
	e.Timestamp = back.Timestamp // filled? no — Emitter fills; zero here stays zero
	require.Equal(t, e, back)
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	e := Event{
		Type: EventRunFinished, Timestamp: 1720000000000,
		ThreadID: "thread-1", RunID: "run-1", ParentRunID: "run-0",
		Usage: &Usage{Input: 3, Output: 5, Total: 8, Cached: 1, Reasoning: 2},
		Model: "model-x", Metadata: map[string]string{"k": "v"},
		Result: map[string]any{"ok": true},
	}
	raw, err := Encode(e)
	require.NoError(t, err)
	back, err := Decode(raw)
	require.NoError(t, err)
	require.Equal(t, e, back)
}

func TestForwardCompat(t *testing.T) {
	raw := []byte(`{"type":"SUPER_FUTURE_X","threadId":"thread-1","runId":"run-1","delta":"hi"}`)
	e, err := Decode(raw)
	require.NoError(t, err, "Decode must accept unknown types")
	require.Equal(t, EventType("SUPER_FUTURE_X"), e.Type)

	l := openRun(t)
	_, err = l.Append(e)
	require.Error(t, err, "Validate must reject unknown types on Append")

	msgs := Fold([]Event{
		{Type: EventTextMessageStart, MessageID: "m-1", Role: "assistant"},
		{Type: "SUPER_FUTURE_X", MessageID: "m-1", Delta: "pollutes?"},
		{Type: EventTextMessageContent, MessageID: "m-1", Delta: "hi"},
	})
	require.Equal(t, []Message{{ID: "m-1", Role: "assistant", Text: "hi"}}, msgs, "Fold ignores unknown types")
}

func TestAppendIdempotent(t *testing.T) {
	l := openRun(t)
	e := Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant", Timestamp: 42}
	first, err := l.Append(e)
	require.NoError(t, err)
	require.Equal(t, int64(2), first.Seq) // run start took Seq 1
	second, err := l.Append(e)
	require.NoError(t, err)
	require.Equal(t, first.Seq, second.Seq, "exact duplicate returns existing Seq")
	require.Len(t, l.List(), 2, "no gap consumed by the duplicate")
}

func TestSeqConcurrent(t *testing.T) {
	l := openRun(t)
	const writers, per = 8, 25
	for w := 0; w < writers; w++ { // pre-open one segment per writer
		_, err := l.Append(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1",
			MessageID: fmt.Sprintf("m-%d", w), Role: "assistant"})
		require.NoError(t, err)
	}
	base := len(l.List())
	var mu sync.Mutex
	seqs := make([]int64, 0, writers*per)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				stored, err := l.Append(Event{Type: EventTextMessageContent,
					ThreadID: "thread-1", RunID: "run-1",
					MessageID: fmt.Sprintf("m-%d", w), Delta: fmt.Sprintf("w%d-%d;", w, i)})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				seqs = append(seqs, stored.Seq)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	require.Len(t, seqs, writers*per)
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, s := range seqs {
		require.Equal(t, int64(base+1+i), s, "Seq 1-based contiguous under concurrency")
	}
}

func TestUsageRidesTerminal(t *testing.T) {
	l := NewLog("thread-1")
	em := NewEmitter(l)
	require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-1"}))
	u := &Usage{Input: 3, Output: 5, Total: 8}
	require.NoError(t, em.FinishRun(u, "model-x"))
	events := l.List()
	require.Len(t, events, 2)
	term := events[1]
	require.Equal(t, u, term.Usage)
	require.Equal(t, "model-x", term.Model)
	raw, err := Encode(term)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"usage"`)
	back, err := Decode(raw)
	require.NoError(t, err)
	require.Equal(t, term, back, "terminal usage survives the round-trip")
}

// TestFoldGolden drives a multi-run thread through the Emitter: text + tool,
// then a reasoning run, then an error run with partial content.
func TestFoldGolden(t *testing.T) {
	l := NewLog("thread-1")
	em := NewEmitter(l)

	require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-1"}))
	require.NoError(t, em.StartText("m-1", "assistant"))
	require.NoError(t, em.WriteText("m-1", "Hello"))
	require.NoError(t, em.StartTool("m-1", "t-1", "search"))
	require.NoError(t, em.WriteArgs("t-1", `{"q":"x"`))
	require.NoError(t, em.WriteArgs("t-1", `}`))
	require.NoError(t, em.EndTool("t-1"))
	require.NoError(t, em.ToolResult("m-1", "t-1", map[string]any{"ok": true}))
	require.NoError(t, em.WriteText("m-1", " done"))
	require.NoError(t, em.EndText("m-1"))
	require.NoError(t, em.FinishRun(&Usage{Input: 3, Output: 5, Total: 8}, "model-x"))

	require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-2", ParentRunID: "run-1"}))
	require.NoError(t, em.StartReasoning("m-2"))
	require.NoError(t, em.WriteReasoning("m-2", "thinking..."))
	require.NoError(t, em.EndReasoning("m-2"))
	require.NoError(t, em.FinishRun(nil, "model-x"))

	require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-3", ParentRunID: "run-2"}))
	require.NoError(t, em.StartText("m-3", "assistant"))
	require.NoError(t, em.WriteText("m-3", "half"))
	require.NoError(t, em.FailRun("boom", "E_MODEL"))

	want := []Message{
		{ID: "m-1", Role: "assistant", Text: "Hello done",
			ToolCalls: []ToolCall{{ID: "t-1", Name: "search", Args: `{"q":"x"}`, Result: map[string]any{"ok": true}}}},
		{ID: "m-2", Role: "reasoning", Text: "thinking..."},
		{ID: "m-3", Role: "assistant", Text: "half"}, // error run keeps partial content
	}
	require.Equal(t, want, l.Fold())
	require.Equal(t, want, Fold(l.List()), "package-level Fold matches Log.Fold")
}

func TestFoldIgnoresOrphans(t *testing.T) {
	msgs := Fold([]Event{
		{Type: EventTextMessageContent, MessageID: "ghost", Delta: "no start"},
		{Type: EventTextMessageEnd, MessageID: "ghost"},
	})
	require.Empty(t, msgs)
}

// Runnable docs.

func ExampleNewEmitter() {
	l := NewLog("thread-1")
	em := NewEmitter(l)
	_ = em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-1"})
	_ = em.StartText("m-1", "assistant")
	_ = em.WriteText("m-1", "Hello")
	_ = em.EndText("m-1")
	_ = em.FinishRun(&Usage{Input: 3, Output: 5, Total: 8}, "model-x")
	for _, m := range l.Fold() {
		fmt.Printf("%s %s: %s\n", m.ID, m.Role, m.Text)
	}
	// Output: m-1 assistant: Hello
}

func ExampleFold() {
	msgs := Fold([]Event{
		{Type: EventTextMessageStart, MessageID: "m-1", Role: "assistant"},
		{Type: EventTextMessageContent, MessageID: "m-1", Delta: "Hel"},
		{Type: EventTextMessageChunk, MessageID: "m-1", Delta: "lo"},
		{Type: EventReasoningMessageStart, MessageID: "m-2"},
		{Type: EventReasoningMessageContent, MessageID: "m-2", Delta: "hmm"},
	})
	for _, m := range msgs {
		fmt.Printf("%s %s: %s\n", m.ID, m.Role, m.Text)
	}
	// Output:
	// m-1 assistant: Hello
	// m-2 reasoning: hmm
}

func ExampleEncode() {
	raw, _ := Encode(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-1"})
	fmt.Println(strings.Contains(string(raw), `"type":"RUN_STARTED"`))
	back, _ := Decode(raw)
	fmt.Println(back.Type, back.ThreadID, back.RunID)
	// Output:
	// true
	// RUN_STARTED thread-1 run-1
}
