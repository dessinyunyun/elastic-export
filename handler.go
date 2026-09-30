package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type EngineDetailHandler struct {
	service *EngineDetailService
}

func NewEngineDetailHandler(service *EngineDetailService) *EngineDetailHandler {
	return &EngineDetailHandler{service: service}
}

func (h *EngineDetailHandler) Search(c *gin.Context) {
	details, err := h.service.Search(c.Request.Context(), c.Query("q"), c.Query("applicationID"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	WriteResponse(c, http.StatusOK, details)
}

func (h *EngineDetailHandler) GetByApplicationID(c *gin.Context) {
	details, err := h.service.GetByApplicationID(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	WriteResponse(c, http.StatusOK, details)
}

//====================================================================

func (h *EngineDetailHandler) Health(c *gin.Context) {
	if err := h.service.Health(c.Request.Context()); err != nil {
		WriteResponse(c, http.StatusServiceUnavailable, gin.H{"health": "unavailable"})
		return
	}
	WriteResponse(c, http.StatusOK, gin.H{"health": "ok"})
}
