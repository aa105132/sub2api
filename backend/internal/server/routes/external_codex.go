package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterExternalCodexRoutes(
	r *gin.Engine,
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	adminAuth middleware.AdminAuthMiddleware,
) {
	if h == nil || h.ExternalCodex == nil {
		return
	}

	groups := []*gin.RouterGroup{
		r.Group("/api/external/codex"),
		v1.Group("/external/codex"),
	}

	for _, group := range groups {
		group.GET("/status", h.ExternalCodex.PublicStatus)
		group.POST("/direct-push", h.ExternalCodex.DirectPush)
		group.POST("/status", h.ExternalCodex.Status)
		group.POST("/team/vacancies", h.ExternalCodex.TeamVacancies)
		group.POST("/team/info", h.ExternalCodex.TeamInfo)
		group.POST("/team/invite", h.ExternalCodex.TeamInvite)
		group.POST("/team/kick", h.ExternalCodex.TeamKick)
		group.POST("/team/cleanup", h.ExternalCodex.TeamCleanup)

		adminGroup := group.Group("")
		adminGroup.Use(gin.HandlerFunc(adminAuth))
		adminGroup.GET("/auth-url", h.ExternalCodex.AuthURL)
		adminGroup.POST("/callback", h.ExternalCodex.Callback)
	}
}
