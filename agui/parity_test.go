package agui

// Spec-1.0 parity contracts: one group per new family. These run alongside
// the frozen 20-type suite, which stays unmodified.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func openBoth(t *testing.T, runID string) (*Log, *Emitter) {
	t.Helper()
	l := NewLog("thread-1")
	em := NewEmitter(l)
	require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: runID}))
	return l, em
}

func TestPatchOps(t *testing.T) {
	base := map[string]any{"a": 1, "nested": map[string]any{"list": []any{"x", "y"}}}

	t.Run("add replace remove", func(t *testing.T) {
		next, skipped, err := ApplyJSONPatch(base, []PatchOp{
			{Op: "add", Path: "/b", Value: 2},
			{Op: "replace", Path: "/a", Value: 9},
			{Op: "remove", Path: "/nested/list/0"},
		})
		require.NoError(t, err)
		require.Empty(t, skipped)
		require.Equal(t, map[string]any{"a": 9, "b": 2, "nested": map[string]any{"list": []any{"y"}}}, next)
		require.Equal(t, 1, base["a"], "failed or applied patches never mutate the input")
	})

	t.Run("atomic rollback", func(t *testing.T) {
		_, _, err := ApplyJSONPatch(base, []PatchOp{
			{Op: "add", Path: "/b", Value: 2},
			{Op: "remove", Path: "/missing"},
		})
		require.Error(t, err)
		require.Equal(t, 1, base["a"])
		require.NotContains(t, base, "b")
	})

	t.Run("unknown op stripped", func(t *testing.T) {
		next, skipped, err := ApplyJSONPatch(base, []any{
			map[string]any{"op": "nope", "path": "/a"},
			map[string]any{"op": "replace", "path": "/a", "value": 5},
		})
		require.NoError(t, err)
		require.Equal(t, []int{0}, skipped)
		require.Equal(t, 5, next.(map[string]any)["a"])
	})

	t.Run("malformed fatal", func(t *testing.T) {
		_, _, err := ApplyJSONPatch(base, "nope")
		require.Error(t, err)
		_, _, err = ApplyJSONPatch(base, []any{map[string]any{"path": "/a"}})
		require.Error(t, err, "missing op")
		_, _, err = ApplyJSONPatch(base, []any{map[string]any{"op": "remove"}})
		require.Error(t, err, "missing path")
	})

	t.Run("move copy test", func(t *testing.T) {
		next, _, err := ApplyJSONPatch(base, []PatchOp{
			{Op: "move", From: "/nested/list/0", Path: "/nested/list/-"},
			{Op: "copy", From: "/a", Path: "/c"},
			{Op: "test", Path: "/c", Value: 1},
		})
		require.NoError(t, err)
		require.Equal(t, []any{"y", "x"}, next.(map[string]any)["nested"].(map[string]any)["list"])
		require.Equal(t, 1, next.(map[string]any)["c"])
	})
}

func TestStateFamily(t *testing.T) {
	t.Run("snapshot replaces wholesale", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.SnapshotState(map[string]any{"a": 1}))
		require.NoError(t, em.SnapshotState(map[string]any{"b": 2}))
		state, ok := l.StateOf("run-1")
		require.True(t, ok)
		require.Equal(t, map[string]any{"b": 2}, state)
	})

	t.Run("delta atomic with resync", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(Event{Type: EventStateSnapshot, ThreadID: "thread-1", RunID: "run-1", Snapshot: map[string]any{"a": 1}})
		require.NoError(t, err)
		_, err = l.Append(Event{Type: EventStateDelta, ThreadID: "thread-1", RunID: "run-1", Patch: []PatchOp{{Op: "add", Path: "/b", Value: 2}}})
		require.NoError(t, err)
		_, err = l.Append(Event{Type: EventStateDelta, ThreadID: "thread-1", RunID: "run-1", Patch: []PatchOp{{Op: "remove", Path: "/missing"}}})
		require.Error(t, err, "failed patch applies nothing")
		state, _ := l.StateOf("run-1")
		require.Equal(t, map[string]any{"a": 1, "b": 2}, state)
		_, err = l.Append(Event{Type: EventStateSnapshot, ThreadID: "thread-1", RunID: "run-1", Snapshot: map[string]any{"fresh": true}})
		require.NoError(t, err, "producer resyncs with a snapshot")
		state, _ = l.StateOf("run-1")
		require.Equal(t, map[string]any{"fresh": true}, state)
	})

	t.Run("input seeds baseline", func(t *testing.T) {
		l := NewLog("thread-1")
		em := NewEmitter(l)
		require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-1", State: map[string]any{"seed": 1}}))
		state, ok := l.StateOf("run-1")
		require.True(t, ok)
		require.Equal(t, map[string]any{"seed": 1}, state)
	})
}

func TestMessagesSnapshot(t *testing.T) {
	seed := []Event{
		{Type: EventTextMessageStart, MessageID: "m-1", Role: "assistant"},
		{Type: EventTextMessageContent, MessageID: "m-1", Delta: "hello"},
		{Type: EventTextMessageStart, MessageID: "m-2", Role: "assistant"},
		{Type: EventTextMessageContent, MessageID: "m-2", Delta: "bye"},
	}
	t.Run("replace append drop", func(t *testing.T) {
		msgs := Fold(append(seed, Event{
			Type: EventMessagesSnapshot,
			Messages: []Message{
				{ID: "m-1", Role: "assistant", Text: "HELLO"},
				{ID: "m-3", Role: "assistant", Text: "new"},
			},
		}))
		require.Len(t, msgs, 2)
		require.Equal(t, "HELLO", msgs[0].Text, "same id replaces in place")
		require.Equal(t, "m-1", msgs[0].ID)
		require.Equal(t, "m-3", msgs[1].ID, "new ids append in snapshot order")
	})

	t.Run("activity and reasoning survive", func(t *testing.T) {
		withExtras := append(seed,
			Event{Type: EventActivitySnapshot, MessageID: "a-1", ActivityType: "search", Content: map[string]any{"p": 1}},
			Event{Type: EventReasoningMessageStart, MessageID: "r-1"},
			Event{Type: EventReasoningMessageContent, MessageID: "r-1", Delta: "hmm"},
		)
		msgs := Fold(append(withExtras, Event{
			Type:     EventMessagesSnapshot,
			Messages: []Message{{ID: "m-1", Role: "assistant", Text: "HELLO"}},
		}))
		ids := []string{msgs[0].ID, msgs[1].ID, msgs[2].ID}
		require.Equal(t, []string{"m-1", "a-1", "r-1"}, ids)
	})

	t.Run("self-revoking roles", func(t *testing.T) {
		withExtras := append(seed,
			Event{Type: EventActivitySnapshot, MessageID: "a-1", ActivityType: "search", Content: map[string]any{"p": 1}},
		)
		msgs := Fold(append(withExtras, Event{
			Type: EventMessagesSnapshot,
			Messages: []Message{
				{ID: "m-1", Role: "assistant", Text: "x"},
				{ID: "a-2", Role: "activity", ActivityType: "other", Content: map[string]any{}},
			},
		}))
		require.Len(t, msgs, 2)
		require.Equal(t, "m-1", msgs[0].ID)
		require.Equal(t, "a-2", msgs[1].ID, "snapshot carrying activity revokes stale ones")
	})

	t.Run("conversation-wide tag rejected", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(Event{Type: EventMessagesSnapshot, ThreadID: "thread-1", RunID: "run-1",
			SubagentRunID: "s-1", Messages: []Message{}})
		require.Error(t, err)
	})
}

func TestActivityFamily(t *testing.T) {
	mkSnap := func(mid, typ string, content map[string]any) Event {
		return Event{Type: EventActivitySnapshot, ThreadID: "thread-1", RunID: "run-1", MessageID: mid, ActivityType: typ, Content: content}
	}
	t.Run("snapshot replace semantics", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(mkSnap("a-1", "search", map[string]any{"p": 1}))
		require.NoError(t, err)
		f := false
		_, err = l.Append(Event{Type: EventActivitySnapshot, ThreadID: "thread-1", RunID: "run-1",
			MessageID: "a-1", ActivityType: "other", Content: map[string]any{"p": 2}, Replace: &f})
		require.NoError(t, err, "replace=false still appends the event")
		typ, content, ok := l.ActivityOf("run-1", "a-1")
		require.True(t, ok)
		require.Equal(t, "search", typ, "replace=false keeps existing")
		require.Equal(t, map[string]any{"p": 1}, content)
	})

	t.Run("delta amends type and content", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.SnapshotActivity("a-1", "search", map[string]any{"p": 1, "q": 2}))
		require.NoError(t, em.PatchActivity("a-1", "browse", []PatchOp{{Op: "replace", Path: "/p", Value: 9}, {Op: "remove", Path: "/q"}}))
		typ, content, ok := l.ActivityOf("run-1", "a-1")
		require.True(t, ok)
		require.Equal(t, "browse", typ, "delta replaces the activity type")
		require.Equal(t, map[string]any{"p": 9}, content)
		msgs := l.Fold()
		require.Len(t, msgs, 1)
		require.Equal(t, "activity", msgs[0].Role)
		require.Equal(t, "browse", msgs[0].ActivityType)
	})

	t.Run("delta for unknown activity is logged but inert", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(Event{Type: EventActivityDelta, ThreadID: "thread-1", RunID: "run-1",
			MessageID: "ghost", ActivityType: "x", Patch: []PatchOp{{Op: "add", Path: "/p", Value: 1}}})
		require.NoError(t, err, "warn-and-skip, never fatal")
		_, _, ok := l.ActivityOf("run-1", "ghost")
		require.False(t, ok)
		require.Empty(t, l.Fold())
	})

	t.Run("delta must stay an object", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(mkSnap("a-1", "search", map[string]any{"p": 1}))
		require.NoError(t, err)
		_, err = l.Append(Event{Type: EventActivityDelta, ThreadID: "thread-1", RunID: "run-1",
			MessageID: "a-1", ActivityType: "x", Patch: []PatchOp{{Op: "replace", Path: "", Value: []any{1}}}})
		require.Error(t, err)
	})
}

func TestSubagentFamily(t *testing.T) {
	t.Run("announce then attribute", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.StartSubagent("s-1"))
		sub := em.ForSubagent("s-1")
		require.NoError(t, sub.StartText("m-1", "assistant"))
		require.NoError(t, sub.WriteText("m-1", "hi from sub"))
		require.NoError(t, sub.EndText("m-1"))
		require.NoError(t, em.FinishSubagent("s-1", ""))
		msgs := l.Fold()
		require.Len(t, msgs, 1)
		require.Equal(t, "hi from sub", msgs[0].Text)
	})

	t.Run("announce-before-attribute", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1",
			MessageID: "m-1", Role: "assistant", SubagentRunID: "never-announced"})
		require.Error(t, err)
	})

	t.Run("ownership agrees with opener", func(t *testing.T) {
		_, em := openBoth(t, "run-1")
		require.NoError(t, em.StartSubagent("s-1"))
		require.NoError(t, em.StartSubagent("s-2"))
		sub := em.ForSubagent("s-1")
		require.NoError(t, sub.StartText("m-1", "assistant"))
		require.Error(t, em.WriteText("m-1", "untagged"), "opener was tagged")
		other := em.ForSubagent("s-2")
		require.Error(t, other.WriteText("m-1", "wrong owner"), "tag must match opener")
		require.NoError(t, sub.WriteText("m-1", "mine"))
	})

	t.Run("no reuse in run, continuable across runs", func(t *testing.T) {
		l := NewLog("thread-1")
		em := NewEmitter(l)
		require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-1"}))
		require.NoError(t, em.StartSubagent("s-1"))
		require.Error(t, em.StartSubagentNested("s-1", "p"), "id names the invocation, not the agent")
		require.NoError(t, em.FinishSubagent("s-1", OutcomeSuspended))
		require.NoError(t, em.FinishRun(nil, "m"))
		require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-2"}))
		require.NoError(t, em.StartSubagent("s-1"), "suspended invocations continue under the same id")
		require.NoError(t, em.FinishSubagent("s-1", ""))
		require.NoError(t, em.FinishRun(nil, "m"))
	})

	t.Run("run must not finish with open invocations", func(t *testing.T) {
		_, em := openBoth(t, "run-1")
		require.NoError(t, em.StartSubagent("s-1"))
		require.Error(t, em.FinishRun(nil, "m"))
		require.NoError(t, em.FailRun("boom", "E"), "RUN_ERROR abandons open invocations")
	})

	t.Run("usage stays on the parent", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(Event{Type: EventSubagentFinished, ThreadID: "thread-1", RunID: "run-1",
			SubagentRunID: "s-1", Usage: &Usage{Input: 1}})
		require.Error(t, err)
	})
}

func TestPassthroughFamily(t *testing.T) {
	t.Run("standalone anywhere in open run", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.EmitRaw(map[string]any{"token": 1}, "openai"))
		require.NoError(t, em.EmitCustom("acme.progress", map[string]any{"pct": 50}))
		require.Len(t, l.List(), 3)
		require.Empty(t, l.Fold(), "passthrough carries no message content")
	})

	t.Run("custom closes chunk lane only", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.StartText("m-1", "assistant"))
		require.NoError(t, em.WriteText("m-1", "a"))
		require.NoError(t, em.EmitCustom("acme.mark", map[string]any{"at": 1}))
		require.NoError(t, em.WriteText("m-1", "b"), "content lane stays open across CUSTOM")
		require.NoError(t, em.EndText("m-1"))
		msgs := l.Fold()
		require.Len(t, msgs, 1)
		require.Equal(t, "ab", msgs[0].Text)
	})

	t.Run("unknown custom ignored not stripped", func(t *testing.T) {
		l, _ := openBoth(t, "run-1")
		_, err := l.Append(Event{Type: EventCustom, ThreadID: "thread-1", RunID: "run-1", CustomName: "future.thing", CustomValue: map[string]any{"v": 1}})
		require.NoError(t, err)
		back, err := Encode(l.List()[1])
		require.NoError(t, err)
		dec, err := Decode(back)
		require.NoError(t, err)
		require.Equal(t, "future.thing", dec.CustomName, "event preserved verbatim")
		require.Empty(t, Fold(l.List()), "no behavior derived")
	})
}

func TestEncryptedValue(t *testing.T) {
	t.Run("attaches to known entities", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.StartText("m-1", "assistant"))
		require.NoError(t, em.WriteText("m-1", "hi"))
		require.NoError(t, em.EndText("m-1"))
		require.NoError(t, em.StartTool("m-1", "t-1", "search"))
		require.NoError(t, em.EndTool("t-1"))
		require.NoError(t, em.StoreEncryptedValue("message", "m-1", "blob-m"))
		require.NoError(t, em.StoreEncryptedValue("toolCall", "t-1", "blob-t"))
		msgs := l.Fold()
		require.Equal(t, "blob-m", msgs[0].EncryptedValue)
		require.Equal(t, "blob-t", msgs[0].ToolCalls[0].EncryptedValue)
	})

	t.Run("unknown entity skipped", func(t *testing.T) {
		msgs := Fold([]Event{
			{Type: EventReasoningEncryptedValue, RunID: "r", Subtype: "message", EntityID: "ghost", EncryptedValue: "x"},
		})
		require.Empty(t, msgs, "warn-and-skip, never fatal")
	})
}

func TestStreamLifecycle(t *testing.T) {
	t.Run("stream opens with started or error", func(t *testing.T) {
		l := NewLog("thread-1")
		_, err := l.AppendStream(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant"})
		require.Error(t, err)
		_, err = l.AppendStream(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
	})

	t.Run("fail-before-begin", func(t *testing.T) {
		l := NewLog("thread-1")
		_, err := l.AppendStream(Event{Type: EventRunError, ThreadID: "thread-1", RunID: "run-0", Message: "dial failed"})
		require.NoError(t, err)
		_, err = l.AppendStream(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-0", MessageID: "m-1", Role: "assistant"})
		require.Error(t, err, "failed run stays closed")
	})

	t.Run("late error after close", func(t *testing.T) {
		l := NewLog("thread-1")
		_, err := l.AppendStream(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
		_, err = l.AppendStream(Event{Type: EventRunFinished, ThreadID: "thread-1", RunID: "run-1"})
		require.NoError(t, err)
		_, err = l.AppendStream(Event{Type: EventRunError, ThreadID: "thread-1", RunID: "run-1", Message: "flush failed"})
		require.NoError(t, err, "late RUN_ERROR records the failure")
		_, err = l.AppendStream(Event{Type: EventTextMessageStart, ThreadID: "thread-1", RunID: "run-1", MessageID: "m-1", Role: "assistant"})
		require.Error(t, err, "run stays closed after the late error")
		_, err = l.AppendStream(Event{Type: EventRunStarted, ThreadID: "thread-1", RunID: "run-2"})
		require.NoError(t, err, "next run still opens")
	})
}

func TestRunEndShape(t *testing.T) {
	t.Run("absent outcome is success", func(t *testing.T) {
		e := Event{Type: EventRunFinished, RunID: "r"}
		require.NoError(t, e.Validate())
		require.Equal(t, OutcomeSuccess, e.OutcomeOrSuccess())
	})

	t.Run("interrupt needs entries", func(t *testing.T) {
		require.Error(t, Event{Type: EventRunFinished, RunID: "r", Outcome: OutcomeInterrupt}.Validate())
		e := Event{Type: EventRunFinished, RunID: "r", Outcome: OutcomeInterrupt,
			Interrupts: []Interrupt{{ID: "i-1", Reason: "approval"}}}
		require.NoError(t, e.Validate())
		require.Error(t, Event{Type: EventRunFinished, RunID: "r", Outcome: "bogus"}.Validate())
	})

	t.Run("pending rides success only", func(t *testing.T) {
		require.NoError(t, Event{Type: EventRunFinished, RunID: "r", PendingToolCallIDs: []string{"t-1"}}.Validate())
		require.Error(t, Event{Type: EventRunFinished, RunID: "r", Outcome: OutcomeCancelled, PendingToolCallIDs: []string{"t-1"}}.Validate())
	})

	t.Run("usage per provider and model", func(t *testing.T) {
		require.Error(t, Event{Type: EventRunFinished, RunID: "r", Usages: []ProviderUsage{{Model: "m"}}}.Validate())
		require.NoError(t, Event{Type: EventRunFinished, RunID: "r",
			Usages: []ProviderUsage{{Provider: "openai", Model: "gpt-x", InputTokens: 3, OutputTokens: 5, TotalTokens: 8}}}.Validate())
	})

	t.Run("resume covers every interrupt", func(t *testing.T) {
		its := []Interrupt{{ID: "i-1"}, {ID: "i-2"}}
		in := RunInput{Resume: []ResumeEntry{{InterruptID: "i-1", Status: ResumeAnswered}, {InterruptID: "i-2", Status: ResumeAbandoned}}}
		require.NoError(t, in.ValidateResume(its))
		require.Error(t, RunInput{Resume: []ResumeEntry{{InterruptID: "i-1", Status: ResumeAnswered}}}.ValidateResume(its), "every interrupt covered")
		require.Error(t, RunInput{Resume: []ResumeEntry{{InterruptID: "i-9", Status: ResumeAnswered}}}.ValidateResume(its[:1]), "no unknown entries")
		require.Error(t, RunInput{Resume: []ResumeEntry{{InterruptID: "i-1", Status: "maybe"}}}.ValidateResume(its[:1]), "answered|abandoned only")
	})

	t.Run("interrupt run round-trip", func(t *testing.T) {
		l, em := openBoth(t, "run-1")
		require.NoError(t, em.InterruptRun([]Interrupt{{ID: "i-1", Reason: "approval", Message: "allow?"}}))
		term := l.List()[1]
		require.Equal(t, OutcomeInterrupt, term.OutcomeOrSuccess())
		raw, err := Encode(term)
		require.NoError(t, err)
		back, err := Decode(raw)
		require.NoError(t, err)
		require.Equal(t, term, back)
	})
}

func TestRunInputStripping(t *testing.T) {
	in := RunInput{Messages: []Message{
		{ID: "m-1", Role: "user", Text: "hi"},
		{ID: "a-1", Role: "activity", ActivityType: "search", Content: map[string]any{}},
		{ID: "m-2", Role: "user", Parts: []ContentPart{
			{Type: "text", Text: "see"},
			{Type: "image"},
			{Type: "image", URL: "https://x/y.png"},
		}},
	}}
	stripped := in.StrippedMessages()
	require.Len(t, stripped, 2, "activity never travels to the producer")
	require.Len(t, stripped[1].Parts, 2, "unusable parts skipped")
}

// TestParityGolden drives every new family through the Emitter and pins the
// fold, the run state, and the activity store.
func TestParityGolden(t *testing.T) {
	l := NewLog("thread-1")
	em := NewEmitter(l)

	require.NoError(t, em.StartRun(RunInput{ThreadID: "thread-1", RunID: "run-1"}))
	require.NoError(t, em.SnapshotState(map[string]any{"cart": []any{"a"}}))
	require.NoError(t, em.PatchState([]PatchOp{{Op: "add", Path: "/cart/1", Value: "b"}}))
	require.NoError(t, em.StartText("m-1", "assistant"))
	require.NoError(t, em.WriteText("m-1", "total "))
	require.NoError(t, em.EmitCustom("shop.nudge", map[string]any{"at": 1}))
	require.NoError(t, em.WriteText("m-1", "two"))
	require.NoError(t, em.EndText("m-1"))
	require.NoError(t, em.StartReasoning("r-1"))
	require.NoError(t, em.WriteReasoning("r-1", "hmm"))
	require.NoError(t, em.EndReasoning("r-1"))
	require.NoError(t, em.StoreEncryptedValue("message", "r-1", "blob-r"))
	require.NoError(t, em.SnapshotActivity("a-1", "search", map[string]any{"q": "shoes"}))
	require.NoError(t, em.PatchActivity("a-1", "search", []PatchOp{{Op: "add", Path: "/n", Value: 3}}))
	require.NoError(t, em.StartSubagent("s-1"))
	sub := em.ForSubagent("s-1")
	require.NoError(t, sub.StartText("m-2", "assistant"))
	require.NoError(t, sub.WriteText("m-2", "subtotal"))
	require.NoError(t, sub.EndText("m-2"))
	require.NoError(t, em.FinishSubagent("s-1", ""))
	require.NoError(t, em.EmitRaw("native-token", "acme"))
	require.NoError(t, em.FinishRunEx(RunEnd{
		Usage:  &Usage{Input: 3, Output: 5, Total: 8},
		Usages: []ProviderUsage{{Provider: "acme", Model: "m-1", InputTokens: 3, OutputTokens: 5, TotalTokens: 8}},
		Model:  "m-1",
	}))

	msgs := l.Fold()
	require.Len(t, msgs, 4)
	require.Equal(t, "total two", msgs[0].Text)
	require.Equal(t, "hmm", msgs[1].Text)
	require.Equal(t, "blob-r", msgs[1].EncryptedValue)
	require.Equal(t, "activity", msgs[2].Role)
	require.Equal(t, map[string]any{"q": "shoes", "n": 3}, msgs[2].Content)
	require.Equal(t, "subtotal", msgs[3].Text)

	state, ok := l.StateOf("run-1")
	require.True(t, ok)
	require.Equal(t, map[string]any{"cart": []any{"a", "b"}}, state)
	typ, content, ok := l.ActivityOf("run-1", "a-1")
	require.True(t, ok)
	require.Equal(t, "search", typ)
	require.Equal(t, map[string]any{"q": "shoes", "n": 3}, content)

	// Absent stays absent on the new envelope: no nulls leak onto the wire,
	// and the codec round-trips canonically (typed patches decode untyped).
	for _, e := range l.List() {
		raw, err := Encode(e)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "null")
		back, err := Decode(raw)
		require.NoError(t, err)
		require.Equal(t, e.Type, back.Type)
		require.Equal(t, e.RunID, back.RunID)
		require.Equal(t, e.Seq, back.Seq)
		rewritten, err := Encode(back)
		require.NoError(t, err)
		require.JSONEq(t, string(raw), string(rewritten))
	}
}
