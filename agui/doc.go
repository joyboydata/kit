// Package chat is the frozen, stdlib-only core of the chat protocol.
//
// Frozen per docs/adr/20261112-2332-chat-protocol-core.md (Phase 1: kit/agui).
//
// Wire format: one JSON object per event, camelCase keys, absent-means-absent
// (every field except "type" carries omitempty, so omitted fields are absent —
// never null). Encode and Decode are total over the envelope: Decode accepts
// event types this version does not know, so old binaries keep reading new
// streams.
//
// Envelope: every event carries threadId (the stable conversation), runId
// (one ephemeral execution; hop-runs chain via parentRunId), and a
// server-assigned seq (1-based, contiguous per Log). Deltas ride the delta
// field; tool results ride result; terminal usage rides usage on RUN_FINISHED;
// failures ride message/code on RUN_ERROR.
//
// Identifiers: threadId names the conversation, runId names one execution,
// messageId names one message slot, toolCallId names one tool invocation,
// stepName names one step. The Emitter mints nothing — callers supply all IDs
// so retries can reuse them and Append can dedupe exact duplicates.
//
// Triad discipline: every streaming family is START → (CONTENT|CHUNK)* → END.
// Content before START, after END, or outside an open run is rejected by
// Append. FINISH/ERROR close exactly the open run they name; a second
// terminal for the same run is rejected.
//
// Fold semantics: Fold derives the user-visible message list from the event
// stream. Text deltas concatenate into Message.Text; tool calls attach to the
// parent messageId (START.delta carries the tool name, ARGS/CHUNK deltas carry
// args fragments, RESULT sets the outcome); reasoning blocks fold into their
// own Message with Role "reasoning"; RUN/STEP frames and unknown types are
// ignored. Fold is outcome-agnostic — a RUN_ERROR never erases partial
// content — and total: orphans and unknown types are skipped, never fatal.
//
// Worked example:
//
//	l := agui.NewLog("thread-1")
//	em := agui.NewEmitter(l)
//	_ = em.StartRun(agui.RunInput{ThreadID: "thread-1", RunID: "run-1"})
//	_ = em.StartText("m-1", "assistant")
//	_ = em.WriteText("m-1", "Hello")
//	_ = em.EndText("m-1")
//	_ = em.FinishRun(&agui.Usage{Input: 3, Output: 5, Total: 8}, "model-x")
//	msgs := l.Fold() // [{ID:m-1 Role:assistant Text:Hello}]
package agui
