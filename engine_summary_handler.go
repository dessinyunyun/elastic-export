package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type EngineSummaryHandler struct {
	service *EngineSummaryService
}

func NewEngineSummaryHandler(service *EngineSummaryService) *EngineSummaryHandler {
	return &EngineSummaryHandler{service: service}
}

func (h *EngineSummaryHandler) Search(c *gin.Context) {
	pagination, err := ParsePagination(c)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	summaries, err := h.service.Search(c.Request.Context(), c.Query("q"), c.Query("applicationID"), pagination)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	WritePaginatedResponse(c, http.StatusOK, summaries)
}

func (h *EngineSummaryHandler) Get(c *gin.Context) {
	summary, err := h.service.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	WriteResponse(c, http.StatusOK, summary)
}
