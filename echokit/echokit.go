// Package echokit provides an opinionated layer over Echo for route
// grouping, registration, and OpenAPI spec generation.
//
// It eliminates the boilerplate of manually creating Echo groups and
// registering routes, replacing it with a declarative Group + Route
// pattern. Route metadata can be annotated with OpenAPI fields for
// automatic Swagger 2.0 spec generation via SpecBuilder.
//
// See https://github.com/labstack/echo for Echo documentation.
//
// Usage:
//
//	import (
//	    "github.com/joyboydata/kit/echokit"
//	    "github.com/labstack/echo/v4"
//	)
//
//	func NewMyRoutes(deps Deps) echokit.Group {
//	    return echokit.Group{
//	        BasePath: "/api/v1/my-resource",
//	        Routes: []echokit.Route{
//	            {
//	                Method:  http.MethodGet,
//	                Pattern: "",
//	                Handler: myListHandler(deps),
//	                // Optional OpenAPI metadata
//	                OpenAPI: &echokit.OpenAPI{
//	                    Summary: "List my resources",
//	                    Tags:    []string{"my-resource"},
//	                },
//	            },
//	        },
//	    }
//	}
//
// Registration:
//
//	e := echo.New()
//	myGroup := NewMyRoutes(deps)
//	myGroup.Register(e)
//
// Spec generation:
//
//	spec := echokit.NewSpecBuilder(echokit.SpecConfig{
//	    Title:   "My API",
//	    Version: "1.0",
//	})
//	spec.AddGroups(myGroup)
//	spec.WriteTo("swagger.json")
package echokit

import (
	"net/http"
	"reflect"

	"github.com/labstack/echo/v4"
)

// OpenAPI holds documentation metadata for a single API route.
// When nil on a Route, the route is excluded from OpenAPI spec generation.
type OpenAPI struct {
	// Summary is a short description of the endpoint.
	Summary string
	// Description is a longer description of the endpoint.
	Description string
	// Tags group this endpoint in the API docs UI.
	Tags []string
	// Body is the request body type for POST/PUT/PATCH endpoints.
	// Use reflect.TypeOf((*MyType)(nil)).Elem().
	// Nil means no request body.
	Body reflect.Type
	// Parameters lists path and query parameters beyond what's extracted
	// automatically from the route pattern.
	Parameters []Parameter
	// Responses maps HTTP status codes to response definitions.
	// If a response Model is nil, it defaults to map[string]string
	// for error responses.
	Responses map[int]ResponseDef
}

// Parameter describes a path or query parameter for OpenAPI documentation.
type Parameter struct {
	// Name is the parameter name as it appears in the URL.
	Name string
	// In specifies where the parameter appears: "path" or "query".
	In string
	// Description explains the parameter's purpose.
	Description string
	// Required indicates whether the parameter is mandatory.
	Required bool
}

// ResponseDef describes an HTTP response for OpenAPI documentation.
type ResponseDef struct {
	// Description explains the response.
	Description string
	// Model is the response body type. Use reflect.TypeOf((*MyType)(nil)).Elem().
	// Nil defaults to map[string]string (used for error responses).
	Model reflect.Type
}

// Tool defines AI agent tool metadata for a route.
// When non-nil, gentool produces a PI extension tool definition
// for this endpoint. Routes without a Tool field are excluded.
type Tool struct {
	// Name is the tool identifier for PI, e.g. "slicer_compute_clv".
	// If empty, the name is auto-generated from the route's OpenAPI Summary.
	Name string

	// Description is usage guidance for the AI model.
	// Unlike OpenAPI.Description (which describes the API endpoint),
	// this describes when and how to use the tool: methodology, preconditions,
	// how to interpret results. Full sentences.
	Description string

	// Example is a JSON string showing a correct invocation body.
	// Rendered verbatim into the generated .ts tool description.
	Example string

	// Disabled explicitly excludes this route from tool generation,
	// even if a Tool value is present (useful for temporarily disabling).
	Disabled bool
}

// Route represents a single HTTP endpoint.
type Route struct {
	Method  string // HTTP method. Defaults to GET if empty.
	Pattern string // URL path relative to Group.BasePath.
	Handler echo.HandlerFunc
	OpenAPI *OpenAPI // optional OpenAPI metadata; nil = exclude from spec
	Tool    *Tool    // optional Tool metadata; nil = exclude from tool generation
}

// Group organizes related routes under a common BasePath.
type Group struct {
	BasePath string
	Routes   []Route
}

// Register adds all routes in the group to an Echo instance.
// Routes are registered under the group's BasePath prefix.
func (g Group) Register(e *echo.Echo) {
	if e == nil || len(g.Routes) == 0 {
		return
	}
	var grp *echo.Group
	if g.BasePath != "" {
		grp = e.Group(g.BasePath)
	}
	for _, r := range g.Routes {
		if r.Method == "" {
			r.Method = http.MethodGet
		}
		if grp != nil {
			grp.Add(r.Method, r.Pattern, r.Handler)
		} else {
			e.Add(r.Method, r.Pattern, r.Handler)
		}
	}
}
