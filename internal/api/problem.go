// Package api implements the Airflow-compatible HTTP control plane (ADR 0007).
package api

import "github.com/gin-gonic/gin"

// Problem is an RFC 7807 problem-details response body.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// AbortProblem writes an RFC 7807 problem response and stops the handler chain.
func AbortProblem(c *gin.Context, status int, title, detail string) {
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(status, Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: c.Request.URL.Path,
	})
}
