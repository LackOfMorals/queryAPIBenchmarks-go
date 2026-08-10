package benchmarks

import (
	"context"
	"testing"

	query "github.com/neo4j-contrib/query-go-sdk"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

func TestNewBoltClient(t *testing.T) {
	tests := []struct {
		name       string
		accessMode query.AccessMode
		wantRoute  neo4j.RoutingControl
	}{
		{name: "read access mode routes to readers", accessMode: query.AccessModeRead, wantRoute: neo4j.Read},
		{name: "write access mode routes to writers", accessMode: query.AccessModeWrite, wantRoute: neo4j.Write},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				URL:        "neo4j://localhost:7687",
				Username:   "neo4j",
				Password:   "password",
				Database:   "neo4j",
				AccessMode: tt.accessMode,
			}

			client, err := newBoltClient(cfg)
			if err != nil {
				t.Fatalf("newBoltClient() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })

			bc, ok := client.(*boltClient)
			if !ok {
				t.Fatalf("newBoltClient() returned %T, want *boltClient", client)
			}
			if bc.dbName != cfg.Database {
				t.Errorf("dbName = %q, want %q", bc.dbName, cfg.Database)
			}

			var applied neo4j.ExecuteQueryConfiguration
			bc.routing(&applied)
			if applied.Routing != tt.wantRoute {
				t.Errorf("routing = %v, want %v", applied.Routing, tt.wantRoute)
			}
		})
	}
}

func TestNewBoltClient_RejectsNonBoltScheme(t *testing.T) {
	cfg := Config{URL: "http://localhost:7474", Username: "neo4j", Password: "password", Database: "neo4j"}
	if _, err := newBoltClient(cfg); err == nil {
		t.Error("newBoltClient() with an http:// URL: expected an error, got nil")
	}
}
