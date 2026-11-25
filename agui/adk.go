// Package agui maps ADK session events onto the agui read model: the batch
// content Stream forwards and the corpus verifies. One home for the mapping
// so the stream and the corpus cannot drift.
//
// This file is the standard ADK→AGUI adapter. Agent implementations consume
// it behind Runtime/Thread: fold a session's events with FoldADK, emit the
// content triads with ADKEvents, and close the run with ADKRunEnd (success)
// or ADKRunError (failure). The single-event ProjectADKEvent mirrors the
// apliq projection entry point it was ported from.
package agui

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Terminal outcomes carried by the content-less marker event. Same strings
// the rows side derives per hop (run-<runID>), pinned by the apliq proofs.
const (
	TerminalSuccess = "success"
	TerminalError   = "error"
)

// ADKUsage accumulates token accounting per hop, like the per-hop FinishRun
// accounting (input/output/total only).
type ADKUsage struct {
	Input, Output, Total int
}

// ADKToolCall is one tool call folded from FunctionCall/Response parts:
// duplicate IDs keep the first (kit semantics), args ride the
// marshal→parse round trip, the response attaches by ID wherever it lives.
type ADKToolCall struct {
	ID, Name  string
	Arguments map[string]any
	Result    string
	IsError   bool
}

// ADKBlock is one ordered content block: text (one per part, never
// concatenated), a reasoning fragment (Part.Thought), or a tool call.
type ADKBlock struct {
	Text    string
	Thought string
	Call    *ADKToolCall
}

// ADKMessage is one transcript-grade message folded out of a session
// event: the role (user/assistant/activity) plus the projected text and run.
// The event-stream side merges these onto the run-log transcript;
// session-only content is additive, never a replacement.
type ADKMessage struct {
	Role  string
	Text  string
	RunID string
	// At is the source event's timestamp: the merge reader orders
	// session-only content onto the legacy transcript by timeline.
	At time.Time
	// ID is the transcript identity: the source event ID for user
	// messages, the hop invocation ID (session-<inv>, disambiguated on
	// repeat) for assistant hops.
	ID string
	// Blocks carries the ordered content (tool-call fidelity): one entry
	// per text part plus folded tool calls in feed order. Text stays the
	// joined text for dedupe/compat; pure tool hops carry empty Text.
	// Thought parts fold to Thought blocks, never to Text.
	Blocks []ADKBlock
	// Usage accumulates per hop like the rows side (input/output/total).
	// Terminal enrichment: success/error markers project Model onto the
	// turn's assistant hops and StopReason onto the marker's run hop.
	Usage      *ADKUsage
	Model      string
	StopReason string
	// ActivityType/Content ride role=activity messages only: one per
	// event carrying RequestedInput (human-in-the-loop interrupt).
	ActivityType string
	Content      any
}

// ProjectADKEvent maps one ADK session event to its agui batch content: the
// human/model text and the run (invocation) ID. ok=false for content-less
// events (state-only, compaction records), which carry no transcript text.
// Thought parts read as reasoning, never as text.
func ProjectADKEvent(ev *session.Event) (text, runID string, ok bool) {
	text = ADKText(ev)
	if text == "" {
		return "", "", false
	}
	if ev.InvocationID != "" {
		runID = ev.InvocationID
	}
	return text, runID, true
}

// ADKText reads the human/model text out of an ADK session event.
// Thought parts are reasoning, not transcript text, so they are skipped.
func ADKText(ev *session.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range ev.Content.Parts {
		if p == nil || p.Text == "" || p.Thought {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// FoldADK projects a session's events onto transcript-grade messages in
// order: user-role content → one user message per event; model-role content
// → one assistant message per hop (a non-empty author change closes the hop,
// mirroring the rows side — content-less events included). Content-less
// events carry no transcript text except terminal markers (turn enrichment)
// and interrupt requests (activity messages). Hops with no text or calls
// emit no message. Messages sort by first-event timestamp (stable).
//
// Terminal outcomes ride the content-less marker event AppendTerminal
// writes (turn.<id>.outcome/runId/model/stopReason in StateDelta). Model
// inherits from the earliest marker at/after the hop (the turn-closing
// marker, any outcome); StopReason pins to the runID-matching hop of a
// success marker only.
func FoldADK(evs []*session.Event) []ADKMessage {
	f := &adkFolder{calls: map[string]*adkCallRef{}, seen: map[string]int{}}
	for _, ev := range evs {
		f.feed(ev)
	}
	return f.finish()
}

// ADKEvents renders folded messages as agui content triads ready for
// Log.Append after RUN_STARTED: text → TEXT_MESSAGE_START/CONTENT/END,
// thought → REASONING_START/MESSAGE_START/CONTENT/MESSAGE_END/END,
// calls → TOOL_CALL_START/ARGS/END/RESULT, interrupts → ACTIVITY_SNAPSHOT.
// The terminal event is not included: close the run with ADKRunEnd (via
// Emitter.FinishRunEx) or ADKRunError (via Emitter.FailRunEx) instead, so
// the Emitter keeps single-close ownership. RunID falls back to runID when
// a message carries none; ThreadID stamps every event.
func ADKEvents(threadID, runID string, msgs []ADKMessage) []Event {
	var out []Event
	for _, m := range msgs {
		rid := m.RunID
		if rid == "" {
			rid = runID
		}
		ts := int64(0)
		if !m.At.IsZero() {
			ts = m.At.UnixMilli()
		}
		stamp := func(e Event) Event {
			e.ThreadID, e.RunID, e.Timestamp = threadID, rid, ts
			return e
		}
		switch m.Role {
		case "activity":
			out = append(out, stamp(Event{
				Type: EventActivitySnapshot, MessageID: m.ID,
				ActivityType: m.ActivityType, Content: m.Content,
			}))
			continue
		case "user", "assistant":
		default:
			continue // unknown roles carry no content
		}
		role := m.Role
		if role == "user" {
			// User text folds one message per event: single triad.
			out = append(out, stamp(Event{Type: EventTextMessageStart, MessageID: m.ID, Role: role}))
			for _, b := range m.Blocks {
				if b.Text == "" {
					continue
				}
				out = append(out, stamp(Event{Type: EventTextMessageContent, MessageID: m.ID, Delta: b.Text}))
			}
			out = append(out, stamp(Event{Type: EventTextMessageEnd, MessageID: m.ID}))
			continue
		}
		// Assistant hop: text triad plus reasoning/tool triads in order.
		if m.Text != "" {
			out = append(out, stamp(Event{Type: EventTextMessageStart, MessageID: m.ID, Role: role}))
			for _, b := range m.Blocks {
				if b.Text == "" {
					continue
				}
				out = append(out, stamp(Event{Type: EventTextMessageContent, MessageID: m.ID, Delta: b.Text}))
			}
			out = append(out, stamp(Event{Type: EventTextMessageEnd, MessageID: m.ID}))
		}
		for i, b := range m.Blocks {
			switch {
			case b.Thought != "":
				rid2 := m.ID + "/reasoning-" + strconv.Itoa(i)
				out = append(out, stamp(Event{Type: EventReasoningStart, MessageID: rid2}))
				out = append(out, stamp(Event{Type: EventReasoningMessageStart, MessageID: rid2}))
				out = append(out, stamp(Event{Type: EventReasoningMessageContent, MessageID: rid2, Delta: b.Thought}))
				out = append(out, stamp(Event{Type: EventReasoningMessageEnd, MessageID: rid2}))
				out = append(out, stamp(Event{Type: EventReasoningEnd, MessageID: rid2}))
			case b.Call != nil:
				c := b.Call
				out = append(out, stamp(Event{
					Type: EventToolCallStart, MessageID: m.ID,
					ToolCallID: c.ID, Delta: c.Name, ToolCallName: c.Name,
				}))
				if len(c.Arguments) > 0 {
					if raw, err := json.Marshal(c.Arguments); err == nil {
						out = append(out, stamp(Event{Type: EventToolCallArgs, ToolCallID: c.ID, Delta: string(raw)}))
					}
				}
				out = append(out, stamp(Event{Type: EventToolCallEnd, ToolCallID: c.ID}))
				if c.Result != "" {
					out = append(out, stamp(Event{
						Type: EventToolCallResult, MessageID: m.ID,
						ToolCallID: c.ID, Result: c.Result,
					}))
				}
			}
		}
	}
	return out
}

// ADKRunEnd folds the terminal success enrichment for Emitter.FinishRunEx:
// usage sums the folded hops, model inherits the earliest marker carrying
// one. ok=false when no success marker closes the run (error markers and
// marker-less runs are not success).
func ADKRunEnd(evs []*session.Event) (end RunEnd, ok bool) {
	msgs := FoldADK(evs)
	var total Usage
	var hasUse bool
	var model string
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		if m.Usage != nil {
			total.Input += m.Usage.Input
			total.Output += m.Usage.Output
			total.Total += m.Usage.Total
			hasUse = true
		}
		if model == "" && m.Model != "" {
			model = m.Model
		}
	}
	for _, ev := range evs {
		if ev == nil || ev.Content != nil {
			continue
		}
		if m, good := parseADKMarker(ev); good && m.outcome == TerminalSuccess {
			end = RunEnd{Model: model}
			if hasUse {
				u := total
				end.Usage = &u
			}
			return end, true
		}
	}
	return RunEnd{}, false
}

// ADKRunError folds the terminal failure for Emitter.FailRunEx: an error
// marker closes the run, the message comes from the last ADK event error
// (fallback names the run). ok=false when no error marker is present.
func ADKRunError(evs []*session.Event) (message, code string, ok bool) {
	found := false
	for _, ev := range evs {
		if ev == nil || ev.Content != nil {
			continue
		}
		if m, good := parseADKMarker(ev); good && m.outcome == TerminalError {
			found = true
		}
	}
	if !found {
		return "", "", false
	}
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		if ev == nil {
			continue
		}
		if ev.ErrorMessage != "" {
			return ev.ErrorMessage, ev.ErrorCode, true
		}
	}
	return "agent run failed", "", true
}

// adkFolder carries the fold state across one event list.
type adkFolder struct {
	items   []adkFoldItem
	markers []adkTermMarker
	cur     *adkHop
	seq     int
	hops    int // hops opened so far: marker↔hop correlation (feed order)
	author  string
	calls   map[string]*adkCallRef
	seen    map[string]int
}

type adkFoldItem struct {
	at  int64
	inv string // hop invocation ID: terminal-marker correlation
	ord int    // hop open order: model inherits from the closing marker
	msg ADKMessage
}

// adkTermMarker is one parsed terminal marker: the content-less StateDelta
// event the turn-closer writes. ord is the hop count when the marker fed:
// a turn's hops all opened before its closing marker, so the marker covers
// exactly the hops with lower ord (feed order, not wall clock).
type adkTermMarker struct {
	ord        int
	turnID     string
	runID      string
	outcome    string
	model      string
	stopReason string
}

type adkHop struct {
	firstAt time.Time
	invID   string
	ord     int
	blocks  []ADKBlock
	usage   ADKUsage
	hasUse  bool
}

type adkCallRef struct {
	call *ADKToolCall
}

func (f *adkFolder) feed(ev *session.Event) {
	if ev == nil {
		return
	}
	if ev.Content == nil {
		// Content-less events carry no transcript text — except terminal
		// markers (turn enrichment) and interrupt requests (activity).
		if m, ok := parseADKMarker(ev); ok {
			m.ord = f.hops
			f.markers = append(f.markers, m)
		}
		if ev.RequestedInput != nil {
			f.items = append(f.items, adkFoldItem{at: ev.Timestamp.UnixMilli(), msg: ADKMessage{
				ID: ev.ID, Role: "activity", RunID: ev.InvocationID, At: ev.Timestamp,
				ActivityType: "interrupt",
				Content: map[string]any{
					"interruptId": ev.RequestedInput.InterruptID,
					"message":     ev.RequestedInput.Message,
				},
			}})
		}
		return
	}
	switch ev.Content.Role {
	case "user":
		if text := ADKText(ev); text != "" {
			f.items = append(f.items, adkFoldItem{at: ev.Timestamp.UnixMilli(), msg: ADKMessage{
				ID: ev.ID, Role: "user", Text: text, RunID: ev.InvocationID, At: ev.Timestamp,
				Blocks: []ADKBlock{{Text: text}},
			}})
		}
		// An interrupt request on a content event folds an activity
		// message after the content, so the run can suspend on it.
		if ev.RequestedInput != nil {
			f.items = append(f.items, adkFoldItem{at: ev.Timestamp.UnixMilli(), msg: ADKMessage{
				ID: ev.ID + "/interrupt", Role: "activity", RunID: ev.InvocationID, At: ev.Timestamp,
				ActivityType: "interrupt",
				Content: map[string]any{
					"interruptId": ev.RequestedInput.InterruptID,
					"message":     ev.RequestedInput.Message,
				},
			}})
		}
		return
	case "model":
		// Hop rotation mirrors the rows side: a non-empty author change
		// closes the hop even when this event carries no content.
		if f.cur != nil && ev.Author != "" && ev.Author != f.author {
			f.closeHop()
		}
		f.author = ev.Author
		if ev.UsageMetadata != nil {
			h := f.hopFor(ev)
			h.usage.Input += int(ev.UsageMetadata.PromptTokenCount)
			h.usage.Output += int(ev.UsageMetadata.CandidatesTokenCount)
			h.usage.Total += int(ev.UsageMetadata.TotalTokenCount)
			h.hasUse = true
		}
		for _, part := range ev.Content.Parts {
			if part == nil {
				continue
			}
			// Text wins, then thought, then call, then response. One
			// part feeds exactly one branch.
			switch {
			case part.Text != "" && !part.Thought:
				h := f.hopFor(ev)
				h.blocks = append(h.blocks, ADKBlock{Text: part.Text})
			case part.Text != "" && part.Thought:
				h := f.hopFor(ev)
				h.blocks = append(h.blocks, ADKBlock{Thought: part.Text})
			case part.FunctionCall != nil:
				f.addCall(ev, part.FunctionCall)
			case part.FunctionResponse != nil:
				f.addResult(part.FunctionResponse)
			}
		}
	default:
		// Function responses and tool results arrive as parts, never as
		// roles; anything else carries no transcript text.
	}
}

// hopFor opens the current hop on first content, recording the first
// event's timestamp and invocation ID for ordering/identity.
func (f *adkFolder) hopFor(ev *session.Event) *adkHop {
	if f.cur == nil {
		f.cur = &adkHop{firstAt: ev.Timestamp, invID: ev.InvocationID, ord: f.hops}
		f.hops++
	}
	return f.cur
}

// closeHop emits the current hop unless it carries no text, thought, or
// calls: such hops never form a stream group on the rows side either.
func (f *adkFolder) closeHop() {
	h := f.cur
	f.cur = nil
	if h == nil || len(h.blocks) == 0 {
		return
	}
	id := h.invID
	if id == "" {
		f.seq++
		id = "session-nouser-hop-" + strconv.Itoa(f.seq)
	} else if n := f.seen[h.invID]; n > 0 {
		// InvocationID spans the whole ADK run while the rows side mints
		// one run per hop: disambiguate repeats with a stable suffix.
		id = id + "-hop-" + strconv.Itoa(n+1)
	} else {
		id = "session-" + id
	}
	f.seen[h.invID]++
	var texts []string
	for _, b := range h.blocks {
		if b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	msg := ADKMessage{
		ID: id, Role: "assistant", Text: strings.Join(texts, "\n"),
		RunID: h.invID, At: h.firstAt, Blocks: append([]ADKBlock(nil), h.blocks...),
	}
	if h.hasUse {
		u := h.usage
		msg.Usage = &u
	}
	f.items = append(f.items, adkFoldItem{at: h.firstAt.UnixMilli(), inv: h.invID, ord: h.ord, msg: msg})
}

func (f *adkFolder) finish() []ADKMessage {
	f.closeHop()
	f.applyMarkers()
	sort.SliceStable(f.items, func(i, j int) bool { return f.items[i].at < f.items[j].at })
	out := make([]ADKMessage, 0, len(f.items))
	for i := range f.items {
		out = append(out, f.items[i].msg)
	}
	return out
}

// addCall records one FunctionCall: duplicate IDs keep the first (kit
// semantics), args ride the bridge's marshal→parse round trip (empty args
// parse to nil).
func (f *adkFolder) addCall(ev *session.Event, call *genai.FunctionCall) {
	if call == nil {
		return
	}
	if _, dup := f.calls[call.ID]; dup {
		return
	}
	h := f.hopFor(ev)
	c := &ADKToolCall{ID: call.ID, Name: call.Name, Arguments: roundTripADKArgs(call.Args)}
	h.blocks = append(h.blocks, ADKBlock{Call: c})
	f.calls[call.ID] = &adkCallRef{call: c}
}

// addResult attaches one FunctionResponse by ID wherever its call lives.
// Orphans drop, like kit.
func (f *adkFolder) addResult(resp *genai.FunctionResponse) {
	if resp == nil {
		return
	}
	ref, ok := f.calls[resp.ID]
	if !ok {
		return
	}
	ref.call.Result = renderADKResult(resp.Response)
}

// parseADKMarker reads one terminal marker out of a content-less StateDelta
// event: keys are turn.<turnID>.<field> (outcome, runId, model,
// stopReason). ok=false when the event carries no terminal outcome key.
func parseADKMarker(ev *session.Event) (adkTermMarker, bool) {
	m := adkTermMarker{}
	found := false
	for k, v := range ev.Actions.StateDelta {
		turnID, field, ok := splitADKMarkerKey(k)
		if !ok {
			continue
		}
		if m.turnID == "" {
			m.turnID = turnID
		} else if m.turnID != turnID {
			continue // one marker per turn; mixed keys stay unread
		}
		switch field {
		case "outcome":
			if s, ok := v.(string); ok {
				m.outcome = s
				found = true
			}
		case "runId":
			if s, ok := v.(string); ok {
				m.runID = s
			}
		case "model":
			if s, ok := v.(string); ok {
				m.model = s
			}
		case "stopReason":
			if s, ok := v.(string); ok {
				m.stopReason = s
			}
		}
	}
	if !found || (m.outcome != TerminalSuccess && m.outcome != TerminalError) {
		return adkTermMarker{}, false
	}
	return m, true
}

// splitADKMarkerKey splits turn.<turnID>.<field>: turn IDs never contain
// dots (ULIDs), so the field is the suffix after the last dot.
func splitADKMarkerKey(k string) (turnID, field string, ok bool) {
	rest, found := strings.CutPrefix(k, "turn.")
	if !found {
		return "", "", false
	}
	idx := strings.LastIndex(rest, ".")
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// applyMarkers projects terminal enrichment onto the folded assistant
// messages: model inherits from the earliest marker at/after the hop (the
// turn-closing marker, any outcome); stopReason pins to the runID-matching
// hop of a success marker only (feed order — hops open before their turn's
// marker, not wall clock). User and activity messages never enrich.
func (f *adkFolder) applyMarkers() {
	if len(f.markers) == 0 {
		return
	}
	for i := range f.items {
		it := &f.items[i]
		if it.msg.Role != "assistant" {
			continue
		}
		for _, m := range f.markers {
			if m.ord <= it.ord {
				continue // only the closing marker covers the hop
			}
			if it.msg.Model == "" && m.model != "" {
				it.msg.Model = m.model
			}
			if it.msg.StopReason == "" && it.inv != "" &&
				m.outcome == TerminalSuccess && m.stopReason != "" && m.runID == it.inv {
				it.msg.StopReason = m.stopReason
			}
			if it.msg.Model != "" && (it.inv == "" || it.msg.StopReason != "") {
				break // earliest model wins; stop pins at most one marker
			}
		}
	}
}

// roundTripADKArgs mirrors the ARGS row round trip: marshal the call args,
// then parse back (nil when no ARGS row exists, {"_raw"} fallback when
// unparsable — same as the rows-side parse).
func roundTripADKArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil // bridge writes no ARGS row: parses to nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{"_raw": string(raw)}
	}
	return parsed
}

// renderADKResult mirrors the rows-side result rendering: strings pass
// through, structured results encode as compact JSON.
func renderADKResult(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
