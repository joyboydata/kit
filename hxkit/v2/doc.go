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

See App for the unit of configuration and Resource for routing rules.
*/
package v2
