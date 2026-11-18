package problem

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestEveryProblemCodeIsInOpenAPI keeps the contract honest: every `code` the
// server can put in a problem+json body must be listed in the Problem.code
// enum of docs/openapi.yaml, so clients can switch on it.
func TestEveryProblemCodeIsInOpenAPI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas struct {
				Problem struct {
					Properties struct {
						Code struct {
							Enum []string `yaml:"enum"`
						} `yaml:"code"`
					} `yaml:"properties"`
				} `yaml:"Problem"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	documented := spec.Components.Schemas.Problem.Properties.Code.Enum
	if len(documented) == 0 {
		t.Fatal("Problem.code enum not found in openapi.yaml")
	}

	// Every non-test Go file under internal/ that sets a problem Code.
	var sources []string
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			sources = append(sources, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	codePattern := regexp.MustCompile(`Code:\s*"([a-z_]+)"`)
	checked := 0
	for _, source := range sources {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range codePattern.FindAllSubmatch(body, -1) {
			checked++
			if code := string(match[1]); !slices.Contains(documented, code) {
				t.Errorf("%s returns code %q, which is missing from the Problem.code enum in docs/openapi.yaml", source, code)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no problem codes found in internal/; the pattern no longer matches the code")
	}
}
