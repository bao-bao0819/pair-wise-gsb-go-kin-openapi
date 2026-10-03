package openapi3plan_test

import (
	"context"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3plan"
	"github.com/getkin/kin-openapi/routers"
)

// ExamplePlan shows pairwise planning for an operation whose enum parameters
// are related by a cross-parameter constraint.
func ExamplePlan() {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: demo, version: "1"}
paths:
  /search:
    get:
      operationId: search
      parameters:
        - name: kind
          in: query
          required: true
          schema: {type: string, enum: [user, team]}
        - name: active
          in: query
          required: true
          schema: {type: boolean}
      responses:
        "200": {description: ok}
`))
	if err != nil {
		panic(err)
	}
	pathItem := doc.Paths.Find("/search")
	route := &routers.Route{
		Spec: doc, PathItem: pathItem, Operation: pathItem.Get, Method: "GET", Path: "/search",
	}

	// team searches cannot be filtered by active=false in this fictional API;
	// that is a field relationship no single parameter schema can express.
	constraint := func(values map[string]openapi3plan.Candidate) bool {
		if values["kind"].Value == "team" && values["active"].Value == false {
			return false
		}
		return true
	}

	report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{Constraint: constraint})
	if err != nil {
		panic(err)
	}

	fmt.Printf("cases=%d feasible=%d infeasible=%d undetermined=%d\n",
		len(report.Cases), report.Feasible, report.Infeasible, report.Undetermined)
	for _, c := range report.Cases {
		fmt.Println(c.Request.URL.String())
	}
	// Output:
	// cases=3 feasible=3 infeasible=1 undetermined=0
	// http://example.com/search?active=false&kind=user
	// http://example.com/search?active=true&kind=team
	// http://example.com/search?active=true&kind=user
}
