package agui

// Fold derives the user-visible message list from an event stream, in order of
// first appearance. Text and reasoning deltas concatenate (CONTENT and CHUNK
// alike); tool calls attach to the parent messageId (START.delta carries the
// tool name, ARGS/CHUNK deltas are args fragments, RESULT sets the outcome);
// reasoning blocks become their own Message with Role "reasoning"; activity
// snapshots become Role "activity" messages amended by ACTIVITY_DELTA;
// MESSAGES_SNAPSHOT reconciles the whole list by id (unknown ids append,
// absent ids drop, except activity/reasoning the producer cannot know unless
// the snapshot itself carries that role); encrypted values attach to their
// named message or tool call. RUN/STEP/STATE/SUBAGENT/RAW/CUSTOM frames and
// unknown types carry no message content, so they are skipped.
//
// Fold is total and outcome-agnostic: unknown types, orphan content (no prior
// START), and post-END fragments are ignored rather than fatal, and RUN_ERROR
// never erases partial content.
func Fold(events []Event) []Message {
	var out []Message
	slot := map[string]int{} // messageId -> index in out
	textSealed := map[string]bool{}
	reasonOpen := map[string]bool{}
	argsOpen := map[string]bool{}
	toolAt := map[string]toolLoc{} // toolCallId -> parent slot + position
	actContent := map[string]map[string]any{}

	ensure := func(id, role string) int {
		if i, ok := slot[id]; ok {
			if out[i].Role == "" {
				out[i].Role = role
			}
			return i
		}
		out = append(out, Message{ID: id, Role: role})
		slot[id] = len(out) - 1
		return len(out) - 1
	}

	for _, e := range events {
		switch e.Type {
		case EventTextMessageStart:
			if textSealed[e.MessageID] {
				continue // no reopen after END
			}
			ensure(e.MessageID, e.Role)
		case EventTextMessageContent, EventTextMessageChunk:
			i, ok := slot[e.MessageID]
			if !ok || textSealed[e.MessageID] {
				continue
			}
			out[i].Text += e.Delta
		case EventTextMessageEnd:
			if _, ok := slot[e.MessageID]; ok {
				textSealed[e.MessageID] = true
			}
		case EventToolCallStart:
			if _, dup := toolAt[e.ToolCallID]; dup {
				continue
			}
			name := e.Delta
			if e.ToolCallName != "" {
				name = e.ToolCallName // long spelling wins when present
			}
			i := ensure(e.MessageID, "assistant")
			out[i].ToolCalls = append(out[i].ToolCalls, ToolCall{ID: e.ToolCallID, Name: name})
			toolAt[e.ToolCallID] = toolLoc{msg: i, idx: len(out[i].ToolCalls) - 1}
			argsOpen[e.ToolCallID] = true
		case EventToolCallArgs, EventToolCallChunk:
			loc, ok := toolAt[e.ToolCallID]
			if !ok || !argsOpen[e.ToolCallID] {
				continue
			}
			out[loc.msg].ToolCalls[loc.idx].Args += e.Delta
		case EventToolCallEnd:
			if _, ok := toolAt[e.ToolCallID]; ok {
				argsOpen[e.ToolCallID] = false
			}
		case EventToolCallResult:
			loc, ok := toolAt[e.ToolCallID]
			if !ok {
				continue
			}
			if e.Result != nil {
				out[loc.msg].ToolCalls[loc.idx].Result = e.Result
			} else if e.Content != nil {
				out[loc.msg].ToolCalls[loc.idx].Result = e.Content
			}
		case EventReasoningMessageStart:
			reasonOpen[e.MessageID] = true
			ensure(e.MessageID, "reasoning")
		case EventReasoningMessageContent, EventReasoningMessageChunk:
			i, ok := slot[e.MessageID]
			if !ok || !reasonOpen[e.MessageID] {
				continue
			}
			out[i].Text += e.Delta
		case EventReasoningMessageEnd:
			reasonOpen[e.MessageID] = false
		case EventReasoningEncryptedValue:
			// Standalone: attach to the named entity when known, else skip.
			switch e.Subtype {
			case "message":
				if i, ok := slot[e.EntityID]; ok {
					out[i].EncryptedValue = e.EncryptedValue
				}
			case "toolCall":
				if loc, ok := toolAt[e.EntityID]; ok {
					out[loc.msg].ToolCalls[loc.idx].EncryptedValue = e.EncryptedValue
				}
			}
		case EventMessagesSnapshot:
			out, slot, toolAt, argsOpen = applySnapshot(out, e.Messages)
			for id := range actContent {
				if i, ok := slot[id]; !ok || out[i].Role != "activity" {
					delete(actContent, id) // dropped activities lose content
				}
			}
		case EventActivitySnapshot:
			content, ok := asObject(e.Content)
			if !ok {
				continue // Fold stays total; Validate rejects on writes
			}
			if i, ok := slot[e.MessageID]; ok && out[i].Role == "activity" {
				if e.Replace != nil && !*e.Replace {
					continue // replace=false keeps the existing message
				}
				out[i].ActivityType, out[i].Content = e.ActivityType, content
				actContent[e.MessageID] = content
				continue
			}
			i := ensure(e.MessageID, "activity")
			out[i].ActivityType, out[i].Content = e.ActivityType, content
			actContent[e.MessageID] = content
		case EventActivityDelta:
			i, ok := slot[e.MessageID]
			if !ok || out[i].Role != "activity" {
				continue
			}
			base, ok := actContent[e.MessageID]
			if !ok {
				continue // delta for unknown activity: skip, keep prior
			}
			next, _, err := ApplyJSONPatch(base, e.Patch)
			if err != nil {
				continue
			}
			obj, ok := next.(map[string]any)
			if !ok {
				continue // patched content must stay an object
			}
			out[i].ActivityType, out[i].Content = e.ActivityType, obj
			actContent[e.MessageID] = obj
		default:
			// RUN_*, STEP_*, STATE_*, SUBAGENT_*, RAW, CUSTOM,
			// REASONING_START/END, and unknown types carry no content.
		}
	}
	return out
}

// applySnapshot reconciles the folded list with the producer's full set:
// matching ids replace in place, new ids append in snapshot order, absent ids
// drop — except activity/reasoning messages the producer cannot know, which
// survive unless the snapshot itself carries that role (self-revoking).
func applySnapshot(out []Message, snap []Message) ([]Message, map[string]int, map[string]toolLoc, map[string]bool) {
	var hasActivity, hasReasoning bool
	for _, m := range snap {
		if m.Role == "activity" {
			hasActivity = true
		}
		if m.Role == "reasoning" {
			hasReasoning = true
		}
	}
	byID := make(map[string]Message, len(snap))
	for _, m := range snap {
		byID[m.ID] = m
	}
	seen := map[string]bool{}
	next := make([]Message, 0, len(out)+len(snap))
	for _, m := range out {
		if sm, ok := byID[m.ID]; ok {
			next, seen[m.ID] = append(next, sm), true // replace in place
			continue
		}
		if m.Role == "activity" && !hasActivity {
			next = append(next, m) // client-only: producer never saw it
			continue
		}
		if m.Role == "reasoning" && !hasReasoning {
			next = append(next, m) // producer typically does not track
			continue
		}
		// Absent and producer-owned: dropped.
	}
	for _, m := range snap {
		if !seen[m.ID] {
			next, seen[m.ID] = append(next, m), true
		}
	}
	slot := make(map[string]int, len(next))
	toolAt := map[string]toolLoc{}
	argsOpen := map[string]bool{}
	for i, m := range next {
		slot[m.ID] = i
		for j, tc := range m.ToolCalls {
			toolAt[tc.ID] = toolLoc{msg: i, idx: j}
			argsOpen[tc.ID] = false // snapshot tools arrive complete
		}
	}
	return next, slot, toolAt, argsOpen
}

// toolLoc pins a tool call to its parent message slot and position, recorded
// once at TOOL_CALL_START.
type toolLoc struct {
	msg int
	idx int
}
