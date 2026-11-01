/*
Package v2 is the resource-oriented successor to hxkit v1.

v1 split routes into Pages and Fragments; v2 drops that split. Every
response is a representation of a resource, selected by a single query
key (?view=<name>), with layout, errors, and server-sent events applied
as framework policy from the declaration.

A resource declares its representations, mutations, and streams in one
place. Handlers share one signature and return a templ component (or an
error carrying a status code); the framework decides full-page vs
fragment rendering, redirect conventions, error wire shape, and SSE
framing. The Watch seam keeps kit independent of any
datastore: the app adapts its stores to Watch at the call site,
reading request state (such as the auth user) from echo.Context.

Sub-paths (Mutation.Path, Stream.Path) are always relative to the
resource path: a leading "/" is stripped and "" means the resource
path itself. There is no absolute override — a leading-/ typo once
registered PATCH /:id at root and 404'd the real route with no signal.
Off-prefix endpoints are declared as their own Resource. Dump
App.Routes after Register in tests and deploy checks to audit the
final method+path table.

Unmatched paths are deliberately left to Echo: v2 registers no
catch-all because branding is app-owned (one host's 404 page is
another's wrong brand, and layout-less apps have no shell to wrap).
The escape hatch is raw Echo, registered after v2:

	e.RouteNotFound("/*", branded404) // app-owned 404 page, last so it shadows nothing
	e.HTTPErrorHandler = htmxAwareHandler // optional: HTMX-aware fragment errors

Framework 404s that do exist (unknown ?view, direct EmbedOnly visits)
follow the App error policy like any handler error, so they need no
catch-all to look right.

See App for the unit of configuration and Resource for routing rules.
*/
package v2
