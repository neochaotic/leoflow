package api

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/workspace"
)

// WorkspaceFS is the workspace-confined filesystem backing the Lite web editor
// (ADR 0025). Every path is relative to the workspace root and confined to it.
type WorkspaceFS interface {
	Tree() ([]workspace.Entry, error)
	Read(rel string) ([]byte, error)
	Write(rel string, data []byte) error
	Create(rel string, dir bool) error
	Delete(rel string) error
}

// ideTreeDTO is the response for GET /api/v2/ide/tree.
type ideTreeDTO struct {
	Entries []workspace.Entry `json:"entries"`
}

// ideFileDTO is the response for reading a file.
type ideFileDTO struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ideWriteBody is the PUT payload to overwrite a file's contents.
type ideWriteBody struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ideCreateBody is the POST payload to create a new file or directory.
type ideCreateBody struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// registerIDE mounts the Lite web editor's workspace filesystem API. When fs is
// nil — Production, or Lite without a workspace configured — the routes are not
// registered, so the editor is unavailable (404). Reads require read:dag and
// mutations require write:dag, since the workspace holds DAG source.
func registerIDE(r gin.IRouter, store WorkspaceFS) {
	if store == nil {
		return
	}
	r.GET("/api/v2/ide/tree", RequirePermission("read", "dag"), ideTreeHandler(store))
	r.GET("/api/v2/ide/file", RequirePermission("read", "dag"), ideReadHandler(store))
	r.PUT("/api/v2/ide/file", RequirePermission("write", "dag"), ideWriteHandler(store))
	r.POST("/api/v2/ide/file", RequirePermission("write", "dag"), ideCreateHandler(store))
	r.DELETE("/api/v2/ide/file", RequirePermission("write", "dag"), ideDeleteHandler(store))
}

func ideTreeHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		entries, err := store.Tree()
		if err != nil {
			AbortProblem(c, http.StatusInternalServerError, "ide_error", err.Error())
			return
		}
		c.JSON(http.StatusOK, ideTreeDTO{Entries: entries})
	}
}

func ideReadHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Query("path")
		if path == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "query parameter 'path' is required")
			return
		}
		data, err := store.Read(path)
		if err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusOK, ideFileDTO{Path: path, Content: string(data)})
	}
}

func ideWriteHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ideWriteBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Path == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "a JSON body with a non-empty 'path' is required")
			return
		}
		if err := store.Write(body.Path, []byte(body.Content)); err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusOK, ideFileDTO(body))
	}
}

func ideCreateHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ideCreateBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Path == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "a JSON body with a non-empty 'path' is required")
			return
		}
		if err := store.Create(body.Path, body.Dir); err != nil {
			abortIDEError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"path": body.Path, "dir": body.Dir})
	}
}

func ideDeleteHandler(store WorkspaceFS) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Query("path")
		if path == "" {
			AbortProblem(c, http.StatusBadRequest, "invalid_request", "query parameter 'path' is required")
			return
		}
		if err := store.Delete(path); err != nil {
			abortIDEError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// abortIDEError maps a workspace error to the right status: an unsafe path is a
// client error (400), a missing file is 404, anything else is 500.
func abortIDEError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, workspace.ErrUnsafePath):
		AbortProblem(c, http.StatusBadRequest, "invalid_path", err.Error())
	case errors.Is(err, fs.ErrNotExist):
		AbortProblem(c, http.StatusNotFound, "not_found", err.Error())
	default:
		AbortProblem(c, http.StatusInternalServerError, "ide_error", err.Error())
	}
}
