// Command openapigen generates the Swagger 2.0 spec for the Slicer APIs.
// It replaces the swag init -> swagger.json pipeline by discovering
// echokit.Group constructors via AST across all packages in the module.
//
// No hardcoded imports to handler packages — discovery is fully automatic.
//
// Usage: go run ./x/echokit/cmd/openapigen/
package main

import (
	"log"

	"github.com/joyboydata/kit/echokit"
)

func main() {
	log.Println("openapigen: discovering route groups via AST...")

	// Discover all groups in the module via AST.
	groups, err := echokit.DiscoverGroups(echokit.DiscoverConfig{})
	if err != nil {
		log.Fatalf("discovering groups: %v", err)
	}
	log.Printf("openapigen: discovered %d groups", len(groups))

	// Build the spec.
	b := echokit.NewASTSpecBuilder(echokit.SpecConfig{
		Title:       "Slicer APIs",
		Version:     "1.0",
		Description: "Slicer provides a Segment management service to help teams define and manage segment definition as DSL and produce consistent data orchestration metadata to be integrated into the team specific data infrastructure.",
		BasePath:    "/api/v1",
		LogoURL:     "/static/slicer-logo.png",
		TagGroups: []echokit.TagGroup{
			{Name: "Manage Table Metadata", Tags: []string{"userbase-table", "event-table"}},
			{Name: "Manage Segment Definition", Tags: []string{"segments"}},
			{Name: "Compute Metrics", Tags: []string{"clv", "retention"}},
		},
	})

	b.AddGroups(groups...)

	// Write spec to both locations.
	if err := b.WriteTo("static/swagger.json"); err != nil {
		log.Fatalf("writing swagger.json: %v", err)
	}
	if err := b.WriteTo("internal/httpserver/docs/swagger.json"); err != nil {
		log.Fatalf("writing docs/swagger.json: %v", err)
	}

	log.Println("openapigen: swagger.json written to static/ and internal/httpserver/docs/")
}