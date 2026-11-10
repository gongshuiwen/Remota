// Package remota provides the canonical contract resource embedded in releases.
package remota

import _ "embed"

//go:embed docs/contracts/schemas/messages.schema.json
var schema []byte

func ContractSchema() []byte { return append([]byte(nil), schema...) }
