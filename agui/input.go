package agui

import "fmt"

// Run outcomes: absent outcome means success, so producers omit it there.
const (
	OutcomeSuccess   = "success"
	OutcomeInterrupt = "interrupt"
	OutcomeCancelled = "cancelled"
	OutcomeSuspended = "suspended" // subagent invocations only
)

// Resume statuses for answers to interrupts.
const (
	ResumeAnswered  = "answered"
	ResumeAbandoned = "abandoned"
)

// Interrupt names one outside input a run with outcome=interrupt waits on.
type Interrupt struct {
	ID             string `json:"id,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
	ToolCallID     string `json:"toolCallId,omitempty"`
	ResponseSchema any    `json:"responseSchema,omitempty"`
	SubagentRunID  string `json:"subagentRunId,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
}

// ResumeEntry answers (or abandons) one interrupt from a continued run.
type ResumeEntry struct {
	InterruptID string            `json:"interruptId,omitempty"`
	Status      string            `json:"status,omitempty"`
	Payload     any               `json:"payload,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ProviderUsage is token accounting for one provider+model pair; the run is
// the accounting boundary, so subagent calls roll into the parent's list.
type ProviderUsage struct {
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	InputTokens           int    `json:"inputTokens,omitempty"`
	OutputTokens          int    `json:"outputTokens,omitempty"`
	CachedInputTokens     int    `json:"cachedInputTokens,omitempty"`
	CacheWriteInputTokens int    `json:"cacheWriteInputTokens,omitempty"`
	ReasoningTokens       int    `json:"reasoningTokens,omitempty"`
	TotalTokens           int    `json:"totalTokens,omitempty"`
}

// ContentPart is one multimodal history part; Source is one of data/url/file
// and file handles stay opaque (the peer must not fetch them).
type ContentPart struct {
	Type     string `json:"type,omitempty"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
}

// Usable reports whether the part carries anything renderable; producers skip
// parts that do not, instead of failing the run.
func (p ContentPart) Usable() bool {
	switch p.Type {
	case "text":
		return p.Text != ""
	case "":
		return false
	default: // image/audio/video/document/file: inline, remote, or handle
		return p.Data != "" || p.URL != "" || p.Path != ""
	}
}

// FrontendTool is one tool the app offers to the agent for this run.
type FrontendTool struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// PatchOp is one RFC 6902 operation; Event.Patch carries these untyped so
// old binaries still decode new streams.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
}

// RunEnd bundles the terminal RUN_FINISHED shape; absent Outcome is success.
type RunEnd struct {
	Outcome            string
	Result             any
	Usage              *Usage
	Usages             []ProviderUsage
	Model              string
	Interrupts         []Interrupt
	PendingToolCallIDs []string
}

// StrippedMessages returns the run input history minus activity messages
// (which never travel to the producer) with unusable parts dropped.
func (in RunInput) StrippedMessages() []Message {
	out := make([]Message, 0, len(in.Messages))
	for _, m := range in.Messages {
		if m.Role == "activity" {
			continue
		}
		kept := m
		if len(m.Parts) > 0 {
			kept.Parts = nil
			for _, p := range m.Parts {
				if p.Usable() {
					kept.Parts = append(kept.Parts, p)
				}
			}
		}
		out = append(out, kept)
	}
	return out
}

// ValidateResume enforces the coverage rule: every interrupt gets exactly one
// answered-or-abandoned entry, and no entry names an unknown interrupt.
// Capabilities stay informative-only: nothing here rejects on them.
func (in RunInput) ValidateResume(interrupts []Interrupt) error {
	seen := map[string]int{}
	for _, r := range in.Resume {
		if r.InterruptID == "" {
			return fmt.Errorf("agui: resume entry requires interruptId")
		}
		if r.Status != ResumeAnswered && r.Status != ResumeAbandoned {
			return fmt.Errorf("agui: resume entry %q requires status answered|abandoned", r.InterruptID)
		}
		seen[r.InterruptID]++
	}
	for _, it := range interrupts {
		n := seen[it.ID]
		delete(seen, it.ID)
		if n != 1 {
			return fmt.Errorf("agui: interrupt %q needs exactly one resume entry", it.ID)
		}
	}
	for id := range seen {
		return fmt.Errorf("agui: resume entry %q names no interrupt", id)
	}
	return nil
}
