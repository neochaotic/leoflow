package storage

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/neochaotic/leoflow/internal/domain"
)

// airflowConnURI renders a connection as an Airflow connection URI
// (conn_type://login:password@host:port/schema), the form Airflow's env secrets
// backend parses from AIRFLOW_CONN_<ID>. Login/password are percent-encoded.
func airflowConnURI(c domain.Connection) string {
	u := url.URL{Scheme: c.ConnType}
	if c.Login != "" || c.Password != "" {
		u.User = url.UserPassword(c.Login, c.Password)
	}
	host := c.Host
	if c.Port != nil {
		host = fmt.Sprintf("%s:%d", c.Host, *c.Port)
	}
	u.Host = host
	if c.Schema != "" {
		// sqlite's canonical URI is `sqlite:///<absolute path>` (3 slashes).
		// The operator may type the path with or without a leading slash; if
		// we always prepend `/` we double up and emit `sqlite:////...` which
		// SQLAlchemy and `urlparse(uri).path` parse incorrectly. Idempotent
		// handling is a no-op for postgres/mysql/etc. (their schema names
		// never start with `/`).
		if strings.HasPrefix(c.Schema, "/") {
			u.Path = c.Schema
		} else {
			u.Path = "/" + c.Schema
		}
	}
	// Airflow carries the connection's extra (a JSON blob) in the URI under the
	// __extra__ query param; without this, extra params (sslmode, etc.) are lost.
	if c.Extra != "" {
		u.RawQuery = url.Values{"__extra__": {c.Extra}}.Encode()
	}
	return u.String()
}
