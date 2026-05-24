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
