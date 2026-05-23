package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// FavoriteStore persists per-user DAG favorites (the DAG-list star).
type FavoriteStore interface {
	AddFavorite(ctx context.Context, tenant, userID, dagID string) error
	RemoveFavorite(ctx context.Context, tenant, userID, dagID string) error
	FavoriteDagIDs(ctx context.Context, tenant, userID string) (map[string]bool, error)
}

// favoriteUserID returns a stable id for the current user to scope favorites by,
// falling back to the email then "anonymous" so the star always has an owner.
func favoriteUserID(c *gin.Context) string {
	if u, ok := UserFromContext(c); ok {
		if u.ID != "" {
			return u.ID
		}
		if u.Email != "" {
			return u.Email
		}
	}
	return "anonymous"
}

// favoriteHandler toggles a DAG's favorite mark for the current user. add=true
// implements POST /api/v2/dags/{dag_id}/favorite, add=false the unfavorite.
func favoriteHandler(store FavoriteStore, add bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var err error
		if add {
			err = store.AddFavorite(c.Request.Context(), tenantOf(c), favoriteUserID(c), c.Param("dag_id"))
		} else {
			err = store.RemoveFavorite(c.Request.Context(), tenantOf(c), favoriteUserID(c), c.Param("dag_id"))
		}
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// registerUIFavorites mounts the favorite/unfavorite endpoints the DAG-list star
// posts to. No-op when no store is configured.
func registerUIFavorites(r gin.IRouter, store FavoriteStore) {
	if store == nil {
		return
	}
	r.POST("/api/v2/dags/:dag_id/favorite", RequirePermission("write", "dag"), favoriteHandler(store, true))
	r.POST("/api/v2/dags/:dag_id/unfavorite", RequirePermission("write", "dag"), favoriteHandler(store, false))
}
