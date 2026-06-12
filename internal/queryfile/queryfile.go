// Package queryfile loads a TOML file that defines a named set of Cypher
// queries to benchmark.
package queryfile

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Query is a single named Cypher statement from the query file.
type Query struct {
	Label  string `toml:"label"`
	Cypher string `toml:"cypher"`
}

type file struct {
	Queries []Query `toml:"queries"`
}

// Load parses path as a TOML query file and returns its queries.
// Every entry must have a non-empty label and cypher.
func Load(path string) ([]Query, error) {
	var f file
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("query file %q: %w", path, err)
	}
	if len(f.Queries) == 0 {
		return nil, fmt.Errorf("query file %q: no [[queries]] entries found", path)
	}
	for i, q := range f.Queries {
		if q.Label == "" {
			return nil, fmt.Errorf("query file %q: entry %d missing label", path, i)
		}
		if q.Cypher == "" {
			return nil, fmt.Errorf("query file %q: entry %d (%q) missing cypher", path, i, q.Label)
		}
	}
	return f.Queries, nil
}
