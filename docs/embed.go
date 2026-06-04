package providersdocs

import "embed"

// FS contains the embedded provider documentation files.
//
//go:embed *.md
var FS embed.FS
