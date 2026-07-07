// Package migrations embeds the SQL migration files so they ship inside the
// single binary. Kept as its own package purely so //go:embed can reach the
// db/migrations/*.sql files (embed only sees paths at or below its own dir).
package migrations

import "embed"

//go:embed migrations/*.sql
var FS embed.FS
