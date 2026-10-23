package bootstrap

import _ "embed"

// exampleConfig keeps the documented configuration template checked against the
// copy shipped with the desktop source.
//
//go:embed config.example.jsonc
var exampleConfig string
