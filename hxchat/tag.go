package hxchat

import (
	"context"
	"html"
	"io"
	"net/url"
	"strings"

	"github.com/a-h/templ"

	agui "github.com/joyboydata/kit/agui"
)

// Tag renders the mount: one <chat-thread> carrying the thread, prefix,
// theme, and relative src/stream/post URLs, with the current thread
// HTML as its light-DOM children for pre-JS paint. Render it on a page
// served from Options.Path so the relative URLs resolve.
func (c *Chat) Tag(m Mount) templ.Component {
	if m.Prefix == "" {
		m.Prefix = SanitizePrefix(m.ThreadID)
	}
	if m.Theme == "" {
		m.Theme = c.opts.Theme
	}
	store := c.store
	render := func(ctx context.Context, w io.Writer, m Mount, msgs []agui.Message) error {
		return c.renderThread(m, msgs, false, RunState{Phase: PhaseIdle}, nil).Render(ctx, w)
	}
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		msgs := agui.Fold(store.List(m.ThreadID))
		var b strings.Builder
		b.WriteString(`<chat-thread thread="`)
		b.WriteString(html.EscapeString(m.ThreadID))
		b.WriteString(`" prefix="`)
		b.WriteString(html.EscapeString(m.Prefix))
		b.WriteString(`"`)
		if m.Theme != "" {
			b.WriteString(` theme="`)
			b.WriteString(html.EscapeString(m.Theme))
			b.WriteString(`"`)
		}
		b.WriteString(` src="?view=thread" stream="`)
		b.WriteString(html.EscapeString(streamRef(m)))
		b.WriteString(`" post="`)
		b.WriteString(html.EscapeString(postRef(m)))
		b.WriteString(`">`)
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
		if err := render(ctx, w, m, msgs); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</chat-thread>`)
		return err
	})
}

// Head renders the one-time asset includes: the stylesheet and the
// element definition. Emit once per page, typically beside the layout
// head; Tag instances share it.
func (c *Chat) Head() templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w,
			`<link rel="stylesheet" href="`+assetCSS+`">`+
				`<script src="`+assetJS+`" defer></script>`)
		return err
	})
}

// StreamURL returns the stream URL relative to the page, matching the
// stream attribute Tag renders. The element appends ?hxchatns= per
// instance for frame isolation.
func (c *Chat) StreamURL(m Mount) string {
	return streamRef(m)
}

// streamRef and postRef re-anchor the thread segment so the refs resolve
// to the resource path from the canonical page URL (see postAction).
func streamRef(m Mount) string {
	if m.ThreadID == "" {
		return "stream"
	}
	return url.PathEscape(m.ThreadID) + "/stream"
}

func postRef(m Mount) string {
	if m.ThreadID == "" {
		return "messages"
	}
	return url.PathEscape(m.ThreadID) + "/messages"
}
