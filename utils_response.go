package main

import (
	"errors"
	"log"
	"net/http"

	"elastic-testing/export"

	"github.com/gin-gonic/gin"
)

type APIResponse struct {
	Status int             `json:"status"`
	Data   any             `json:"data"`
	Meta   *PaginationMeta `json:"meta,omitempty"`
}

func WriteResponse(c *gin.Context, status int, data any) {
	c.JSON(status, APIResponse{Status: status, Data: data})
}

func WritePaginatedResponse[T any](c *gin.Context, status int, result *PaginatedData[T]) {
	c.JSON(status, APIResponse{Status: status, Data: result.Items, Meta: &result.Meta})
}

func WriteError(c *gin.Context, status int, message string) {
	WriteResponse(c, status, gin.H{"message": message})
}

func writeServiceError(c *gin.Context, err error) {
	if errors.Is(err, ErrSummaryNotFound) {
		WriteError(c, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrInvalidPagination) || errors.Is(err, export.ErrInvalidFilter) {
		WriteError(c, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("engine service: %v", err)
	WriteError(c, http.StatusBadGateway, "Elasticsearch request failed")
}
