package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// VariableStore reads and writes Airflow-style Variables for the Admin UI.
type VariableStore interface {
	ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error)
	GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error)
	SetVariable(ctx context.Context, tenant string, v domain.Variable) error
	DeleteVariable(ctx context.Context, tenant, key string) error
}

// sensitiveKeyParts mark a variable whose value is masked in API responses, so
// secrets are not echoed back to the UI (mirrors Airflow's default).
var sensitiveKeyParts = []string{
	"secret", "password", "passwd", "passphrase", "token",
	"apikey", "api_key", "access_key", "private_key", "authorization", "credential",
}

// maskedValue returns "***" when the key looks sensitive, else the value.
func maskedValue(key, value string) string {
	lower := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lower, part) {
			return "***"
		}
	}
	return value
}

// variableDTO is the Airflow 3.2.1 VariableResponse. Leoflow stores variables in
// plaintext for now (is_encrypted is false) and has no teams (team_name null).
type variableDTO struct {
	Key         string  `json:"key"`
	Value       string  `json:"value"`
	Description *string `json:"description"`
	IsEncrypted bool    `json:"is_encrypted"`
	TeamName    *string `json:"team_name"`
}

type variableCollectionDTO struct {
	Variables    []variableDTO `json:"variables"`
	TotalEntries int           `json:"total_entries"`
}

func toVariableDTO(v domain.Variable) variableDTO {
	return variableDTO{
		Key:         v.Key,
		Value:       maskedValue(v.Key, v.Value),
		Description: strPtrOrNil(v.Description),
		IsEncrypted: false,
		TeamName:    nil,
	}
}

// listVariablesHandler implements GET /api/v2/variables.
func listVariablesHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		vars, total, err := store.ListVariables(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := variableCollectionDTO{Variables: make([]variableDTO, 0, len(vars)), TotalEntries: total}
		for _, v := range vars {
			out.Variables = append(out.Variables, toVariableDTO(v))
		}
		c.JSON(http.StatusOK, out)
	}
}

// getVariableHandler implements GET /api/v2/variables/{variable_key}.
func getVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := store.GetVariable(c.Request.Context(), tenantOf(c), c.Param("variable_key"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toVariableDTO(v))
	}
}

// createVariableHandler implements POST /api/v2/variables.
func createVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Key         string `json:"key"`
			Value       string `json:"value"`
			Description string `json:"description"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.Key == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "key is required")
			return
		}
		v := domain.Variable{Key: body.Key, Value: body.Value, Description: body.Description}
		if err := store.SetVariable(c.Request.Context(), tenantOf(c), v); err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusCreated, toVariableDTO(v))
	}
}

// updateVariableHandler implements PATCH /api/v2/variables/{variable_key}.
func updateVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.Param("variable_key")
		if _, err := store.GetVariable(c.Request.Context(), tenantOf(c), key); err != nil {
			handleRepoError(c, err)
			return
		}
		var body struct {
			Value       string `json:"value"`
			Description string `json:"description"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		v := domain.Variable{Key: key, Value: body.Value, Description: body.Description}
		if err := store.SetVariable(c.Request.Context(), tenantOf(c), v); err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toVariableDTO(v))
	}
}

// deleteVariableHandler implements DELETE /api/v2/variables/{variable_key}.
func deleteVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := store.DeleteVariable(c.Request.Context(), tenantOf(c), c.Param("variable_key")); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// registerUIVariables mounts the Admin Variables CRUD when a store is set;
// otherwise the empty stub stands in so the Admin page still renders.
func registerUIVariables(r gin.IRouter, store VariableStore) {
	if store == nil {
		r.GET("/api/v2/variables", apiEmptyCollection("variables"))
		return
	}
	r.GET("/api/v2/variables", RequirePermission("read", "variable"), listVariablesHandler(store))
	r.GET("/api/v2/variables/:variable_key", RequirePermission("read", "variable"), getVariableHandler(store))
	r.POST("/api/v2/variables", RequirePermission("write", "variable"), createVariableHandler(store))
	r.PATCH("/api/v2/variables/:variable_key", RequirePermission("write", "variable"), updateVariableHandler(store))
	r.DELETE("/api/v2/variables/:variable_key", RequirePermission("write", "variable"), deleteVariableHandler(store))
}
