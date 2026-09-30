package main

import (
	"context"
	"strings"
)

type EngineSummaryService struct {
	repo *EngineSummaryRepo
}

func NewEngineSummaryService(repo *EngineSummaryRepo) *EngineSummaryService {
	return &EngineSummaryService{repo: repo}
}

func (s *EngineSummaryService) Search(ctx context.Context, term, applicationID string, pagination Pagination) (*PaginatedData[EngineSummary], error) {
	if err := pagination.Validate(); err != nil {
		return nil, err
	}
	items, total, err := s.repo.Search(ctx, strings.TrimSpace(term), strings.TrimSpace(applicationID), pagination)
	if err != nil {
		return nil, err
	}
	return NewPaginatedData(items, total, pagination), nil
}

func (s *EngineSummaryService) Get(ctx context.Context, id string) (*EngineSummary, error) {
	return s.repo.Get(ctx, id)
}
