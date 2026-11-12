package chat

// Fold derives the user-visible message list from an event slice, in order of
// first appearance. Text and reasoning deltas concatenate (CONTENT and CHUNK
// alike); tool calls attach to the parent messageId (START.delta is the tool
// name, ARGS/CHUNK deltas are args fragments, RESULT sets the outcome);
// reasoning blocks become their own Message with Role "reasoning". RUN/STEP
// frames shape the log but carry no message content, so they are skipped.
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
			i := ensure(e.MessageID, "assistant")
			out[i].ToolCalls = append(out[i].ToolCalls, ToolCall{ID: e.ToolCallID, Name: e.Delta})
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
			out[loc.msg].ToolCalls[loc.idx].Result = e.Result
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
		default:
			// RUN_*, STEP_*, REASONING_START/END, and unknown types carry
			// no message content.
		}
	}
	return out
}

// toolLoc pins a tool call to its parent message slot and position, recorded
// once at TOOL_CALL_START.
type toolLoc struct {
	msg int
	idx int
}
