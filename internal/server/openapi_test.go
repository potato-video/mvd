package server

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// api/openapi.yaml documents every API route the server registers (not the
// web UI's pages), and nothing that isn't one.
func TestOpenAPICoversRoutes(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, m := range regexp.MustCompile(`(?m)^  (/\S+):$`).FindAllStringSubmatch(string(spec), -1) {
		documented = append(documented, m[1])
	}
	route := regexp.MustCompile(`mux\.HandleFunc\("(?:GET|POST|PUT|PATCH|DELETE) (/(?:api|stream|subs|art)/[^"]*)"`)
	files, _ := filepath.Glob("*.go")
	var registered []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, m := range route.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(registered, m[1]) {
				registered = append(registered, m[1])
			}
		}
	}
	for _, r := range registered {
		if !slices.Contains(documented, r) {
			t.Errorf("%s isn't in api/openapi.yaml", r)
		}
	}
	for _, d := range documented {
		if !slices.Contains(registered, d) {
			t.Errorf("api/openapi.yaml has %s, which the server doesn't serve", d)
		}
	}
	if len(registered) < 10 {
		t.Errorf("found only %d routes: has the registration code changed shape?", len(registered))
	}
}

// api/openapi.yaml's info.version is the API's level.
func TestOpenAPIVersionIsAPILevel(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^info:\n(?:  .*\n)*?  version: "?(\d+)"?$`).FindSubmatch(spec)
	if m == nil || string(m[1]) != strconv.Itoa(APILevel) {
		t.Errorf("info.version %q, APILevel %d", m, APILevel)
	}
}
