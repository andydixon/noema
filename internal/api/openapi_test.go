package api

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestOpenAPICoversEveryRoute keeps openapi.json honest: every /api/v1 route
// registered in this package must be documented with its method.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPI, &doc); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Handle(?:Func)?\("(GET|POST|PUT|PATCH|DELETE) (/api/v1[^"]*)"`)
	entries, _ := os.ReadDir(".")
	var missing []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, _ := os.ReadFile(e.Name())
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			method, path := strings.ToLower(m[1]), m[2]
			if path == "/api/v1/openapi.json" {
				continue
			}
			if _, ok := doc.Paths[path][method]; !ok {
				missing = append(missing, m[1]+" "+path)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("routes missing from openapi.json (regenerate it):\n%s", strings.Join(missing, "\n"))
	}
}
