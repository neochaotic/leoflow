package storage

import (
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
	if got := airflowConnURI(domain.Connection{ConnID: "h", ConnType: "http", Host: "api.example.com"}); got != "http://api.example.com" {
		t.Errorf("minimal uri = %q, want http://api.example.com", got)
	}
}
