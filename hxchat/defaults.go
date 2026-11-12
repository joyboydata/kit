package hxchat

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/url"
	"strings"

	"github.com/a-h/templ"

	"github.com/joyboydata/kit/agui"
)

// DefaultThreadView renders the four regions (messages, composer,
// status, error) with the stock slots and an unlocked composer. The
// live lock arrives over the stream; Chat's handler renders the same
// shape with the current lock instead.
func DefaultThreadView(m Mount, msgs []agui.Message) templ.Component {
	return renderThread(defaultSlots(), m, msgs, false, RunState{Phase: PhaseIdle}, nil)
}

// DefaultMessageView renders one folded message: role-stamped text plus
// any tool calls (name, args, outcome). Text is escaped; the wire is
// HTML, never client-templated JSON.
func DefaultMessageView(m Mount, msg agui.Message) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		var b strings.Builder
		b.WriteString(`<div class="hxchat-msg" data-role="`)
		b.WriteString(html.EscapeString(msg.Role))
		b.WriteString(`" data-message-id="`)
		b.WriteString(html.EscapeString(msg.ID))
		b.WriteString(`"><p>`)
		b.WriteString(html.EscapeString(msg.Text))
		b.WriteString(`</p>`)
		for _, tc := range msg.ToolCalls {
			b.WriteString(`<div class="hxchat-tool" data-tool-call-id="`)
			b.WriteString(html.EscapeString(tc.ID))
			b.WriteString(`"><span class="hxchat-tool-name">`)
			b.WriteString(html.EscapeString(tc.Name))
			b.WriteString(`</span>`)
			if tc.Args != "" {
				b.WriteString(`<pre class="hxchat-tool-args">`)
				b.WriteString(html.EscapeString(tc.Args))
				b.WriteString(`</pre>`)
			}
			if tc.Result != nil {
				b.WriteString(`<pre class="hxchat-tool-result">`)
				b.WriteString(html.EscapeString(renderResult(tc.Result)))
				b.WriteString(`</pre>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
		_, err := io.WriteString(w, b.String())
		return err
	})
}

// DefaultComposerView renders the composer form posting to the relative
// "{thread}/messages" URL. Locked disables the controls and names the
// state; the element additionally guards double-submit client-side.
func DefaultComposerView(m Mount, locked bool) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		var b strings.Builder
		b.WriteString(`<form class="hxchat-composer" data-chat-composer method="post" action="`)
		b.WriteString(html.EscapeString(postAction(m)))
		b.WriteString(`">`)
		b.WriteString(`<textarea name="text" rows="2" placeholder="Write a message...`)
		b.WriteString(`"`)
		if locked {
			b.WriteString(` disabled aria-disabled="true"`)
		}
		b.WriteString(`></textarea>`)
		b.WriteString(`<button type="submit"`)
		if locked {
			b.WriteString(` disabled aria-disabled="true"`)
		}
		b.WriteString(`>`)
		if locked {
			b.WriteString(`Working...`)
		} else {
			b.WriteString(`Send`)
		}
		b.WriteString(`</button></form>`)
		_, err := io.WriteString(w, b.String())
		return err
	})
}

// DefaultStatusView renders the run phase for assistive tech and
// styling hooks. The running frame names the run; idle names none.
func DefaultStatusView(m Mount, s RunState) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		var b strings.Builder
		b.WriteString(`<div class="hxchat-status" data-phase="`)
		b.WriteString(html.EscapeString(s.Phase))
		b.WriteString(`" role="status">`)
		switch s.Phase {
		case PhaseRunning:
			b.WriteString(`Working...`)
			if s.RunID != "" {
				b.WriteString(`<span class="hxchat-run" data-run-id="`)
				b.WriteString(html.EscapeString(s.RunID))
				b.WriteString(`"></span>`)
			}
		case PhaseError:
			b.WriteString(`Something went wrong.`)
		default:
			b.WriteString(`Ready.`)
		}
		b.WriteString(`</div>`)
		_, err := io.WriteString(w, b.String())
		return err
	})
}

// DefaultErrorView renders the failure plus a retry form. Retry posts a
// new run with parent_run set, chaining hop-runs via ParentRunID. A nil
// error renders the generic text; the run ID rides hidden, never shown.
func DefaultErrorView(m Mount, err error, runID string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		msg := "The run failed."
		if err != nil && err.Error() != "" {
			msg = err.Error()
		}
		var b strings.Builder
		b.WriteString(`<div class="hxchat-error" role="alert"><p>`)
		b.WriteString(html.EscapeString(msg))
		b.WriteString(`</p><form method="post" action="`)
		b.WriteString(html.EscapeString(postAction(m)))
		b.WriteString(`" data-chat-retry>`)
		b.WriteString(`<input type="hidden" name="text" value="Retry">`)
		b.WriteString(`<input type="hidden" name="parent_run" value="`)
		b.WriteString(html.EscapeString(runID))
		b.WriteString(`"><button type="submit">Retry</button></form></div>`)
		_, werr := io.WriteString(w, b.String())
		return werr
	})
}

// postAction is the composer target relative to the page URL. Tag must be
// rendered on the canonical resource URL (no trailing slash): the thread
// segment re-anchors the ref to the resource path, so "t1/messages" from
// /chat/t1 resolves to /chat/t1/messages.
func postAction(m Mount) string {
	return url.PathEscape(m.ThreadID) + "/messages"
}

// slots is the effective render set: user overrides over the defaults.
// Thread rendering threads the effective message slot through, so a
// RenderMessage decorator applies to every message in the thread.
type slots struct {
	renderMessage  func(m Mount, msg agui.Message) templ.Component
	renderComposer func(m Mount, locked bool) templ.Component
	renderStatus   func(m Mount, s RunState) templ.Component
	renderError    func(m Mount, err error, runID string) templ.Component
}

// defaultSlots collects the stock renderers.
func defaultSlots() slots {
	return slots{
		renderMessage:  DefaultMessageView,
		renderComposer: DefaultComposerView,
		renderStatus:   DefaultStatusView,
		renderError:    DefaultErrorView,
	}
}

// effectiveSlots overlays the configured Render* slots on the defaults.
func (c *Chat) effectiveSlots() slots {
	s := defaultSlots()
	if c.opts.RenderMessage != nil {
		s.renderMessage = c.opts.RenderMessage
	}
	if c.opts.RenderComposer != nil {
		s.renderComposer = c.opts.RenderComposer
	}
	if c.opts.RenderStatus != nil {
		s.renderStatus = c.opts.RenderStatus
	}
	if c.opts.RenderError != nil {
		s.renderError = c.opts.RenderError
	}
	return s
}

// renderThread renders the four regions with the chat's effective slots.
func (c *Chat) renderThread(m Mount, msgs []agui.Message, locked bool, st RunState, rerr *runFailure) templ.Component {
	var errComp templ.Component
	if st.Phase == PhaseError && rerr != nil {
		errComp = c.effectiveSlots().renderError(m, rerr.err, rerr.runID)
	}
	return renderThread(c.effectiveSlots(), m, msgs, locked, st, errComp)
}

// renderThread composes the thread body: each region wrapped in its
// prefixed container so stream frames swap by outerHTML.
func renderThread(s slots, m Mount, msgs []agui.Message, locked bool, st RunState, errComp templ.Component) templ.Component {
	ns := regionNames{base: m.Prefix}
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<div class="hxchat" data-hxchat-thread>`); err != nil {
			return err
		}
		if err := messagesRegion(ns.messages(), m, msgs, s.renderMessage).Render(ctx, w); err != nil {
			return err
		}
		if err := composerRegion(ns.composer(), m, locked, s.renderComposer).Render(ctx, w); err != nil {
			return err
		}
		if err := statusRegion(ns.status(), m, st, s.renderStatus).Render(ctx, w); err != nil {
			return err
		}
		return errorRegion(ns.error(), m, errComp).Render(ctx, w)
	})
}

// messagesRegion wraps the folded messages in their swappable container.
func messagesRegion(id string, m Mount, msgs []agui.Message, render func(Mount, agui.Message) templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		var b strings.Builder
		b.WriteString(`<div id="`)
		b.WriteString(html.EscapeString(id))
		b.WriteString(`" data-chat-region="messages">`)
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
		for _, msg := range msgs {
			if err := render(m, msg).Render(ctx, w); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, `</div>`)
		return err
	})
}

// composerRegion wraps the composer slot in its swappable container.
func composerRegion(id string, m Mount, locked bool, render func(Mount, bool) templ.Component) templ.Component {
	return regionWrap(id, "composer", render(m, locked))
}

// statusRegion wraps the status slot in its swappable container.
func statusRegion(id string, m Mount, st RunState, render func(Mount, RunState) templ.Component) templ.Component {
	return regionWrap(id, "status", render(m, st))
}

// errorRegion wraps the (possibly empty) error body in its container.
func errorRegion(id string, _ Mount, body templ.Component) templ.Component {
	return regionWrap(id, "error", body)
}

// regionWrap frames any slot body in the prefixed swappable container.
func regionWrap(id, region string, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		var b strings.Builder
		b.WriteString(`<div id="`)
		b.WriteString(html.EscapeString(id))
		b.WriteString(`" data-chat-region="`)
		b.WriteString(html.EscapeString(region))
		b.WriteString(`">`)
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
		if body != nil {
			if err := body.Render(ctx, w); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, `</div>`)
		return err
	})
}

// renderResult flattens a tool outcome for display.
func renderResult(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return strings.TrimSpace(strings.Trim(fmt.Sprintf("%v", v), `"`))
}
