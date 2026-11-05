package grammardata

import _ "embed"

const Revision = "2346a3ab1bb3857b48b29d779a1ef9799a248cd7"

//go:embed grammar.json
var Grammar []byte

//go:embed node-types.json
var NodeTypes []byte

//go:embed SOURCE.json
var Source []byte
