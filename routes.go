package main

import (
	"context"
	"net/http"
	"time"

	"elastic-testing/export"

	"github.com/gin-gonic/gin"
)

func NewRoutes(handler *EngineDetailHandler, summaryHandler *EngineSummaryHandler, dummyHandler *DummyHandler, exportHandler *export.ExportHandler) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.CustomRecovery(func(c *gin.Context, recovered any) {
		c.Abort()
		if !c.Writer.Written() {
			WriteError(c, http.StatusInternalServerError, "internal server error")
		}
	}))
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) { WriteError(c, http.StatusNotFound, "route not found") })
	router.NoMethod(func(c *gin.Context) { WriteError(c, http.StatusMethodNotAllowed, "method not allowed") })
	router.Use(func(c *gin.Context) {
		timeout := 10 * time.Second
		if c.FullPath() == "/dummy" && c.Request.Method == "POST" {
			timeout = dummyTimeout
		}
		if c.FullPath() == "/export" {
			timeout = export.RequestTimeout
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/health", handler.Health)
	router.GET("/export", exportHandler.Stream)
	router.GET("/engine-detail/:id", handler.GetByApplicationID)
	router.GET("/engine-summary", summaryHandler.Search)
	router.GET("/engine-summary/:id", summaryHandler.Get)
	router.POST("/dummy", dummyHandler.Generate)
	return router
}
