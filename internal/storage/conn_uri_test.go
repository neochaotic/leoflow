package storage

import (
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestAirflowConnURI(t *testing.T) {
	port := 5432
	got := airflowConnURI(domain.Connection{
		ConnID: "pg", ConnType: "postgres", Host: "db.internal",
		Login: "user", Password: "p@ss word", Port: &port, Schema: "analytics",
	})
	want := "postgres://user:p%40ss%20word@db.internal:5432/analytics"
	if got != want {
		t.Errorf("uri = %q, want %q", got, want)
	}
	// No port / no schema / no creds.
	if bare := airflowConnURI(domain.Connection{ConnID: "h", ConnType: "http", Host: "api.example.com"}); bare != "http://api.example.com" {
		t.Errorf("minimal uri = %q, want http://api.example.com", bare)
	}
	// Extra is carried under __extra__ so Airflow recovers it.
	got = airflowConnURI(domain.Connection{ConnID: "p", ConnType: "postgres", Host: "h", Extra: `{"sslmode":"require"}`})
	if !strings.Contains(got, "__extra__=") || !strings.Contains(got, "sslmode") {
		t.Errorf("uri = %q, want extra carried under __extra__", got)
	}
}

// TestAirflowConnURISQLitePath pins the sqlite contract: the Schema field
// carries the database file path, and the builder must render the canonical
// 3-slash form `sqlite:///<absolute path>` whether the operator typed the path
// with or without a leading slash. A double-prepend bug here produces 4
// slashes and breaks SQLAlchemy / `urlparse(...).path` parsing in user DAGs.
func TestAirflowConnURISQLitePath(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name:   "absolute path with leading slash",
			schema: "/var/lib/leoflow/warehouse.db",
			want:   "sqlite:///var/lib/leoflow/warehouse.db",
		},
		{
			name:   "relative path without leading slash",
			schema: "var/lib/leoflow/warehouse.db",
			want:   "sqlite:///var/lib/leoflow/warehouse.db",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := airflowConnURI(domain.Connection{
				ConnID: "sqlite_target", ConnType: "sqlite", Schema: tc.schema,
			})
			if got != tc.want {
				t.Errorf("uri = %q, want %q", got, tc.want)
			}
		})
	}
}
