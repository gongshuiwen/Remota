// Package contracttest checks HTTP exchanges using bindings generated from
// openapi.yaml. OpenAPI references are resolved by the Python contract gate.
package contracttest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"remota.local/remota/internal/protocol"
)

type operation struct {
	Method, Path, Pattern string
	RequestModel          string            `json:"request_model"`
	PathModels            map[string]string `json:"path_models"`
	Responses             map[string]string
	pattern               *regexp.Regexp
}

type responseSpec struct {
	Model   string
	Headers map[string]string
}

type headerSpec struct {
	Required bool
	Schema   map[string]any
	compiled *jsonschema.Schema
}

type Contract struct {
	Headers   map[string]*headerSpec
	Responses map[string]responseSpec
	Entries   []operation `json:"operations"`
	validator *protocol.Validator
}

// Load reads the generated index without a YAML or OpenAPI dependency in Go.
func Load() (*Contract, error) { return load() }

var load = sync.OnceValues(func() (*Contract, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var data []byte
	for {
		data, err = os.ReadFile(filepath.Join(dir, "docs", "contracts", "http.generated.json"))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("HTTP contract index missing; retain docs/contracts beside the tests or in a parent directory")
		}
		dir = parent
	}
	if err != nil {
		return nil, err
	}
	c := new(Contract)
	if err = json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	if c.validator, err = protocol.NewValidator(); err != nil {
		return nil, err
	}
	for i := range c.Entries {
		entry := &c.Entries[i]
		if entry.pattern, err = regexp.Compile(entry.Pattern); err != nil {
			return nil, err
		}
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for name, header := range c.Headers {
		uri := "urn:remota:http-header:" + url.PathEscape(name)
		if err = compiler.AddResource(uri, header.Schema); err != nil {
			return nil, err
		}
		if header.compiled, err = compiler.Compile(uri); err != nil {
			return nil, err
		}
	}
	return c, nil
})

func (c *Contract) Operations() map[string]bool {
	result := make(map[string]bool)
	for _, entry := range c.Entries {
		result[entry.Method+" "+entry.Path] = true
	}
	return result
}

func (c *Contract) message(model string, data []byte) error {
	value, err := protocol.Parse(data)
	if err == nil {
		err = c.validator.Validate(model, value)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", model, err)
	}
	return nil
}

// Check validates the response and the body/path of accepted requests. Actual
// authorization and state transitions remain covered by the domain HTTP tests.
func (c *Contract) Check(request *http.Request, status int, headers http.Header, body []byte) error {
	var op *operation
	var captures, allowed []string
	for i := range c.Entries {
		entry := &c.Entries[i]
		matches := entry.pattern.FindStringSubmatch(request.URL.Path)
		if matches != nil && request.URL.EscapedPath() == request.URL.Path {
			allowed = append(allowed, entry.Method)
			if entry.Method == request.Method {
				op, captures = entry, matches
				break
			}
		}
	}
	key := ""
	if op != nil {
		key = op.Responses[strconv.Itoa(status)]
	} else if len(allowed) != 0 && status == 405 {
		sort.Strings(allowed)
		if headers.Get("Allow") != strings.Join(allowed, ", ") {
			return fmt.Errorf("Allow=%q, expected %v", headers.Get("Allow"), allowed)
		}
		key = "#/components/responses/MethodNotAllowed"
	} else if len(allowed) == 0 && status == 404 {
		key = "#/components/responses/NotFound"
	}
	response, ok := c.Responses[key]
	if !ok {
		return fmt.Errorf("undocumented HTTP response: %s %s -> %d", request.Method, request.URL.Path, status)
	}
	for name, headerKey := range response.Headers {
		header := c.Headers[headerKey]
		if header == nil {
			return fmt.Errorf("missing response header binding: %s", name)
		}
		values := headers.Values(name)
		if len(values) == 0 && !header.Required {
			continue
		}
		if len(values) != 1 {
			return fmt.Errorf("response requires exactly one %s", name)
		}
		if err := header.compiled.Validate(values[0]); err != nil {
			return fmt.Errorf("response header %s: %w", name, err)
		}
	}
	if response.Model == "" {
		if len(body) != 0 {
			return fmt.Errorf("HTTP %d must not include a body", status)
		}
	} else {
		if err := jsonMedia(headers); err != nil {
			return err
		}
		if err := c.message(response.Model, body); err != nil {
			return fmt.Errorf("%s %s -> %d: %w", request.Method, request.URL.Path, status, err)
		}
	}
	if status < 200 || status >= 300 || op == nil {
		return nil
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		return fmt.Errorf("accepted an undeclared query parameter")
	}
	for i, name := range op.pattern.SubexpNames() {
		if model := op.PathModels[name]; model != "" {
			if err := c.validator.Validate(model, captures[i]); err != nil {
				return fmt.Errorf("request parameter %s: %w", name, err)
			}
		}
	}
	var data []byte
	if request.GetBody != nil {
		reader, err := request.GetBody()
		if err != nil {
			return err
		}
		data, err = io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return err
		}
	} else if request.Body != nil && request.Body != http.NoBody {
		return fmt.Errorf("contract check requires a replayable request body")
	}
	if op.RequestModel == "" {
		if len(data) != 0 {
			return fmt.Errorf("accepted an undeclared request body")
		}
		return nil
	}
	if err := jsonMedia(request.Header); err != nil {
		return err
	}
	return c.message(op.RequestModel, data)
}

func jsonMedia(headers http.Header) error {
	contentType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return fmt.Errorf("expected application/json, received %q", headers.Get("Content-Type"))
	}
	return nil
}
