package protocol

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// Exercise the product codec against the canonical data, not a second corpus.
func TestSharedContractMessages(t *testing.T) {
	b, err := os.ReadFile("../../docs/contracts/acceptance/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Valid, Invalid []struct {
			ID, Model string
			Value     json.RawMessage
		}
		Raw []struct {
			ID, Model string
			JSON      *string
			Hex       *string
			Expected  string
		} `json:"raw_json"`
	}
	if err = json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	for group, items := range map[string][]struct {
		ID, Model string
		Value     json.RawMessage
	}{"valid": corpus.Valid, "invalid": corpus.Invalid} {
		for _, item := range items {
			t.Run(group+"/"+item.ID, func(t *testing.T) {
				x, err := Parse(item.Value)
				if err != nil {
					t.Fatal(err)
				}
				if err = v.Validate(item.Model, x); (err == nil) != (group == "valid") {
					t.Fatalf("unexpected validation result: %v", err)
				}
			})
		}
	}
	for _, item := range corpus.Raw {
		t.Run(item.ID, func(t *testing.T) {
			var raw []byte
			if item.JSON != nil {
				raw = []byte(*item.JSON)
			} else {
				raw, err = hex.DecodeString(*item.Hex)
				if err != nil {
					t.Fatal(err)
				}
			}
			x, err := Parse(raw)
			if item.Expected == "invalid_json" {
				if err != InvalidJSON {
					t.Fatalf("invalid JSON accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if item.Model != "" {
				if err = v.Validate(item.Model, x); (err == nil) != (item.Expected == "valid") {
					t.Fatalf("numeric/model validation differs: %v", err)
				}
			}
		})
	}
}
