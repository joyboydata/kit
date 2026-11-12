// Package hxchat mounts the frozen chat protocol as a server-rendered
// widget over hxkit v2: one <chat-thread> tag client-side, one
// Resource() server-side.
//
// # Seams
//
// Store persists one event log per thread and wakes streams; Runner
// executes one run, emitting through the Emitter the Chat hands it.
// The app implements both; the widget owns everything between: the
// user-turn triad, run lifecycle (open synchronously, execute async),
// the forward of runner events into the store, and the fold of stored
// events into region frames.
//
// # Slots
//
// Each region renders through a Render* slot defaulting to its
// Default*View. Unset means stock; set means decorate: call the Default
// inside yours and wrap it. The thread renderer threads the effective
// message slot through, so a RenderMessage override applies to every
// message while composer, status, and error stay stock.
//
// # Widget contract
//
// Attributes: thread (stable conversation), prefix (region scope,
// defaults to the sanitized thread ID), theme (CSS hook), src
// ("?view=thread"), stream ("{thread}/stream"), post
// ("{thread}/messages"). The URLs are relative: render Tag on the
// canonical page served from Options.Path (no trailing slash), so the
// thread segment re-anchors the refs to the resource path.
//
// Regions: {prefix}-messages, {prefix}-composer, {prefix}-status,
// {prefix}-error. The element rewrites them per instance to
// {prefix}-{ns}-{region} on connect, where ns is its auto-minted
// ?hxchatns= query value; the stream prefixes frame names the same
// way, so two mounts on one page never swap each other. The element
// reports its own prefix back as ?prefix=, so frames match Tag's
// per-mount Prefix even when two mounts share a thread.
//
// Wire format: HTML frames over SSE (Replay on connect, Tick debounce,
// identical-frame dedup, Terminal close after the run's terminal
// frame). Streams stay open across quiet threads, so passive mounts
// converge. The composer POST returns Hold with an empty body; the
// stream pushes truth. The element swaps server HTML by region ID and
// never templates JSON.
//
// # Theme and override model
//
// element.css exposes --hxchat-* vars on chat-thread plus a
// data-theme="comic" palette. Override with vars, a theme block, or
// plain selectors (light DOM, no shadow boundary). Slot example:
//
//	RenderComposer: func(m hxchat.Mount, locked bool) templ.Component {
//	    if !currentUser.HasAIAccess() {
//	        return paywallNudge(m)
//	    }
//	    return hxchat.DefaultComposerView(m, locked)
//	},
//
// # Worked mount
//
//	h := hxchat.New(store, runner, hxchat.Options{Path: "/chat/:thread"})
//	app := v2.App{Layout: shell, Resources: h.Resource()}
//	app.Register(e)
//
//	<head>{ h.Head() }</head>
//	<body>{ h.Tag(hxchat.Mount{ThreadID: "abc"}) }</body>
//
// The asset resource (element.js, element.css) serves bytes, so mount
// it on an App with a nil Layout: layouts wrap direct visits in HTML,
// which corrupts scripts. Split Resource() accordingly — widget on the
// main App, assets on a layout-less one sharing the same Echo.
package hxchat
