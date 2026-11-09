package validation

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// All helpers live in test files. No product codec or service is implemented here.
func contractFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "docs", "contracts", filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func strictJSON(b []byte) (any, error) {
	d := jsontext.NewDecoder(bytes.NewReader(b))
	if _, err := d.ReadValue(); err != nil {
		return nil, err
	}
	if _, err := d.ReadValue(); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value: %v", err)
	}
	// This decoder preserves numbers as json.Number for exact schema comparisons.
	return jsonschema.UnmarshalJSON(bytes.NewReader(b))
}

type schemaPattern struct{ re *regexp2.Regexp }

func (r schemaPattern) String() string { return r.re.String() }
func (r schemaPattern) MatchString(s string) bool {
	match, err := r.re.MatchString(s)
	if err != nil {
		panic(err)
	} // A timeout must fail the probe, not masquerade as a rejected input.
	return match
}

type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema load forbidden: %s", url)
}

func schemaCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(offlineLoader{})
	c.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		re.MatchTimeout = time.Second
		return schemaPattern{re}, nil
	})
	v, err := strictJSON(contractFile(t, "schemas/messages.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource("urn:remota:draft2:messages", v); err != nil {
		t.Fatal(err)
	}
	return c
}

type messageExample struct {
	ID     string         `json:"id"`
	Model  string         `json:"model"`
	Value  jsontext.Value `json:"value"`
	Tool   string         `json:"tool,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

func examples(t *testing.T) map[string][]messageExample {
	t.Helper()
	var f struct {
		Valid   []messageExample `json:"valid"`
		Invalid []messageExample `json:"invalid"`
	}
	if err := json.Unmarshal(contractFile(t, "acceptance/messages.json"), &f); err != nil {
		t.Fatal(err)
	}
	return map[string][]messageExample{"valid": f.Valid, "invalid": f.Invalid}
}

func TestContractCorpus(t *testing.T) {
	c := schemaCompiler(t)
	compiled := map[string]*jsonschema.Schema{}
	for group, list := range examples(t) {
		for _, e := range list {
			t.Run(group+"/"+e.ID, func(t *testing.T) {
				s := compiled[e.Model]
				if s == nil {
					var err error
					s, err = c.Compile("urn:remota:draft2:messages#/$defs/" + e.Model)
					if err != nil {
						t.Fatal(err)
					}
					compiled[e.Model] = s
				}
				v, err := strictJSON(e.Value)
				if err != nil {
					t.Fatal(err)
				}
				err = s.Validate(v)
				if (err == nil) != (group == "valid") {
					t.Fatalf("schema expectation differs: %v", err)
				}
			})
		}
	}
}

func TestStrictJSONAndExactNumbers(t *testing.T) {
	var corpus struct {
		RawJSON []struct {
			ID       string  `json:"id"`
			Model    string  `json:"model,omitempty"`
			JSON     *string `json:"json,omitempty"`
			Hex      *string `json:"hex,omitempty"`
			Expected string  `json:"expected"`
		} `json:"raw_json"`
	}
	if err := json.Unmarshal(contractFile(t, "acceptance/messages.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.RawJSON) == 0 {
		t.Fatal("missing raw JSON corpus")
	}
	c := schemaCompiler(t)
	for _, e := range corpus.RawJSON {
		t.Run(e.ID, func(t *testing.T) {
			if (e.JSON == nil) == (e.Hex == nil) {
				t.Fatal("exactly one raw source required")
			}
			var raw []byte
			if e.JSON != nil {
				raw = []byte(*e.JSON)
			} else {
				var err error
				raw, err = hex.DecodeString(*e.Hex)
				if err != nil {
					t.Fatal(err)
				}
			}
			v, err := strictJSON(raw)
			if e.Expected == "invalid_json" {
				if err == nil {
					t.Fatal("invalid JSON accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.Expected != "valid" && e.Expected != "invalid_model" {
				t.Fatal("unknown expectation")
			}
			if e.Model == "" {
				if e.Expected != "valid" {
					t.Fatal("missing model")
				}
				return
			}
			s, err := c.Compile("urn:remota:draft2:messages#/$defs/" + e.Model)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(v); (err == nil) != (e.Expected == "valid") {
				t.Fatalf("schema expectation differs: %v", err)
			}
		})
	}
}
