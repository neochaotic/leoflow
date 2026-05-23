package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// ConnectionStore reads and writes Airflow-style Connections for the Admin UI.
// Password is encrypted at rest by the store (ADR 0019) and never returned.
type ConnectionStore interface {
	ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error)
	GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error)
	SetConnection(ctx context.Context, tenant string, c domain.Connection) error
	DeleteConnection(ctx context.Context, tenant, connID string) error
}

// connectionDTO is the Airflow 3.2.1 ConnectionResponse. The password is never
// included — it is write-only.
type connectionDTO struct {
	ConnectionID string  `json:"connection_id"`
	ConnType     string  `json:"conn_type"`
	Description  *string `json:"description"`
	Host         *string `json:"host"`
	Login        *string `json:"login"`
	Schema       *string `json:"schema"`
	Port         *int    `json:"port"`
	Extra        *string `json:"extra"`
}

type connectionCollectionDTO struct {
	Connections  []connectionDTO `json:"connections"`
	TotalEntries int             `json:"total_entries"`
}

func toConnectionDTO(c domain.Connection) connectionDTO {
	return connectionDTO{
		ConnectionID: c.ConnID,
		ConnType:     c.ConnType,
		Description:  strPtrOrNil(c.Description),
		Host:         strPtrOrNil(c.Host),
		Login:        strPtrOrNil(c.Login),
		Schema:       strPtrOrNil(c.Schema),
		Port:         c.Port,
		Extra:        strPtrOrNil(c.Extra),
	}
}

// connectionBody is the POST/PATCH payload; password is accepted (write-only).
type connectionBody struct {
	ConnectionID string `json:"connection_id"`
	ConnType     string `json:"conn_type"`
	Description  string `json:"description"`
	Host         string `json:"host"`
	Login        string `json:"login"`
	Password     string `json:"password"`
	Schema       string `json:"schema"`
	Port         *int   `json:"port"`
	Extra        string `json:"extra"`
}

func (b connectionBody) toDomain(connID string) domain.Connection {
	return domain.Connection{
		ConnID: connID, ConnType: b.ConnType, Host: b.Host, Schema: b.Schema,
		Login: b.Login, Password: b.Password, Port: b.Port, Extra: b.Extra, Description: b.Description,
	}
}

func listConnectionsHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		conns, total, err := store.ListConnections(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := connectionCollectionDTO{Connections: make([]connectionDTO, 0, len(conns)), TotalEntries: total}
		for _, conn := range conns {
			out.Connections = append(out.Connections, toConnectionDTO(conn))
		}
		c.JSON(http.StatusOK, out)
	}
}

func getConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		conn, err := store.GetConnection(c.Request.Context(), tenantOf(c), c.Param("connection_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toConnectionDTO(conn))
	}
}

func createConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body connectionBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.ConnectionID == "" || body.ConnType == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "connection_id and conn_type are required")
			return
		}
		conn := body.toDomain(body.ConnectionID)
		if err := store.SetConnection(c.Request.Context(), tenantOf(c), conn); err != nil {
			handleConnWriteError(c, err)
			return
		}
		c.JSON(http.StatusCreated, toConnectionDTO(conn))
	}
}

func updateConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		connID := c.Param("connection_id")
		if _, err := store.GetConnection(c.Request.Context(), tenantOf(c), connID); err != nil {
			handleRepoError(c, err)
			return
		}
		var body connectionBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		conn := body.toDomain(connID)
		if conn.ConnType == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "conn_type is required")
			return
		}
		if err := store.SetConnection(c.Request.Context(), tenantOf(c), conn); err != nil {
			handleConnWriteError(c, err)
			return
		}
		c.JSON(http.StatusOK, toConnectionDTO(conn))
	}
}

func deleteConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := store.DeleteConnection(c.Request.Context(), tenantOf(c), c.Param("connection_id")); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// handleConnWriteError reports a missing encryption key as a clear 503 rather
// than a generic 500 (writes are refused without a key — ADR 0019).
func handleConnWriteError(c *gin.Context, err error) {
	if err.Error() == "no encryption key configured" {
		AbortProblem(c, http.StatusServiceUnavailable, "encryption unavailable",
			"set LEOFLOW_SECRET_KEY to manage connections (secrets are never stored in plaintext)")
		return
	}
	handleRepoError(c, err)
}

// registerUIConnections mounts the Admin Connections CRUD when a store is set;
// otherwise an empty-collection stub keeps the Admin page rendering.
func registerUIConnections(r gin.IRouter, store ConnectionStore) {
	if store == nil {
		r.GET("/api/v2/connections", apiEmptyCollection("connections"))
		return
	}
	r.GET("/api/v2/connections", RequirePermission("read", "connection"), listConnectionsHandler(store))
	r.GET("/api/v2/connections/:connection_id", RequirePermission("read", "connection"), getConnectionHandler(store))
	r.POST("/api/v2/connections", RequirePermission("write", "connection"), createConnectionHandler(store))
	r.PATCH("/api/v2/connections/:connection_id", RequirePermission("write", "connection"), updateConnectionHandler(store))
	r.DELETE("/api/v2/connections/:connection_id", RequirePermission("write", "connection"), deleteConnectionHandler(store))
}
