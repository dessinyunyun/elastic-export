package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
)

var ErrInvalidPagination = errors.New("invalid pagination")

type Pagination struct {
	Page  int
	Limit int
}

func (p Pagination) Validate() error {
	if p.Page < 1 || p.Limit < 1 || p.Limit > 100 {
		return fmt.Errorf("%w: page must be >= 1 and size must be between 1 and 100", ErrInvalidPagination)
	}
	if p.Page > int(^uint(0)>>1)/p.Limit {
		return fmt.Errorf("%w: page is too large", ErrInvalidPagination)
	}
	return nil
}

func (p Pagination) Offset() int { return (p.Page - 1) * p.Limit }

func ParsePagination(c *gin.Context) (Pagination, error) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		return Pagination{}, fmt.Errorf("%w: page must be an integer", ErrInvalidPagination)
	}
	// Keep limit as a compatible alias; size takes precedence when both are provided.
	sizeValue := c.DefaultQuery("limit", "10")
	if _, exists := c.Request.URL.Query()["size"]; exists {
		sizeValue = c.Query("size")
	}
	limit, err := strconv.Atoi(sizeValue)
	if err != nil {
		return Pagination{}, fmt.Errorf("%w: size must be an integer", ErrInvalidPagination)
	}
	p := Pagination{Page: page, Limit: limit}
	return p, p.Validate()
}

type PaginatedData[T any] struct {
	Items []T
	Meta  PaginationMeta
}

type PaginationMeta struct {
	Page         int   `json:"page"`
	Size         int   `json:"size"`
	TotalRecords int64 `json:"total_records"`
	TotalPages   int64 `json:"total_pages"`
	HasNext      bool  `json:"has_next"`
	HasPrevious  bool  `json:"has_previous"`
}

func NewPaginatedData[T any](items []T, total int64, p Pagination) *PaginatedData[T] {
	if items == nil {
		items = make([]T, 0)
	}
	pages := total / int64(p.Limit)
	if total%int64(p.Limit) != 0 {
		pages++
	}
	return &PaginatedData[T]{
		Items: items,
		Meta: PaginationMeta{
			Page: p.Page, Size: p.Limit, TotalRecords: total, TotalPages: pages,
			HasNext: int64(p.Page) < pages, HasPrevious: p.Page > 1 && total > 0,
		},
	}
}
