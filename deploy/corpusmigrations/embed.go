// Package corpusmigrations embeds the three independent corpus chains and inventories.
package corpusmigrations

import "embed"

// FS excludes administrator bootstrap SQL.
//
//go:embed writer/*.sql release/*.sql control/*.sql inventory/*.json
var FS embed.FS
