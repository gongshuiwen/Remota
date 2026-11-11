package protocol

import (
	"bytes"
	stdjson "encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"remota.local/remota"
)

var (
	InvalidJSON     = errors.New("INVALID_JSON")
	InvalidArgument = errors.New("INVALID_ARGUMENT")
	UnknownField    = errors.New("UNKNOWN_FIELD")
	InternalError   = errors.New("INTERNAL_ERROR")
)

func Parse(b []byte) (any, error) {
	d := jsontext.NewDecoder(bytes.NewReader(b))
	if _, err := d.ReadValue(); err != nil {
		return nil, InvalidJSON
	}
	if _, err := d.ReadValue(); err != io.EOF {
		return nil, InvalidJSON
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, InvalidJSON
	}
	return v, nil
}

type pattern struct{ re *regexp2.Regexp }

func (p pattern) String() string { return p.re.String() }
func (p pattern) MatchString(s string) bool {
	ok, err := p.re.MatchString(s)
	if err != nil {
		panic(InternalError)
	}
	return ok
}

type offline struct{}

func (offline) Load(string) (any, error) { return nil, errors.New("external schema forbidden") }

type Validator struct{ models map[string]*jsonschema.Schema }

func NewValidator() (*Validator, error) {
	root, err := Parse(remota.ContractSchema())
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(offline{})
	c.UseRegexpEngine(func(s string) (jsonschema.Regexp, error) {
		re, err := regexp2.Compile(s, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		re.MatchTimeout = time.Second
		return pattern{re}, nil
	})
	if err = c.AddResource("urn:remota:draft2:messages", root); err != nil {
		return nil, err
	}
	v := &Validator{models: make(map[string]*jsonschema.Schema)}
	for name := range root.(map[string]any)["$defs"].(map[string]any) {
		s, err := c.Compile("urn:remota:draft2:messages#/$defs/" + name)
		if err != nil {
			return nil, err
		}
		v.models[name] = s
	}
	return v, nil
}

func (v *Validator) Validate(model string, value any) (err error) {
	defer func() {
		if recover() != nil {
			err = InternalError
		}
	}()
	s := v.models[model]
	if s == nil {
		return InternalError
	}
	if err = s.Validate(value); err != nil {
		return InvalidArgument
	}
	return nil
}

// Object validates a parsed object while distinguishing unknown top-level fields.
func (v *Validator) Object(model string, value any) (map[string]any, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, InvalidArgument
	}
	s := v.models[model]
	if s == nil {
		return nil, InternalError
	}
	for field := range m {
		_, known := s.Properties[field]
		for _, variant := range s.OneOf {
			if _, ok := variant.Properties[field]; ok {
				known = true
			}
		}
		if !known {
			return nil, UnknownField
		}
	}
	if err := v.Validate(model, value); err != nil {
		return nil, err
	}
	return m, nil
}

// Integer is called only after Schema has established a bounded integer value.
func Integer(v any) int64 {
	r, ok := new(big.Rat).SetString(v.(stdjson.Number).String())
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		panic(fmt.Errorf("unvalidated integer"))
	}
	return r.Num().Int64()
}
