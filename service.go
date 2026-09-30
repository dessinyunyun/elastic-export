package main

import (
	"context"
	"strings"
)

type EngineDetailService struct {
	repo *EngineDetailRepo
}

func NewEngineDetailService(repo *EngineDetailRepo) *EngineDetailService {
	return &EngineDetailService{repo: repo}
}

func (s *EngineDetailService) Health(ctx context.Context) error {
	return s.repo.Health(ctx)
}

func (s *EngineDetailService) Search(ctx context.Context, term, applicationID string) ([]EngineDetail, error) {
	return s.repo.Search(ctx, strings.TrimSpace(term), strings.TrimSpace(applicationID))
}

func (s *EngineDetailService) GetByApplicationID(ctx context.Context, applicationID string) ([]EngineDetail, error) {
	return s.repo.GetByApplicationID(ctx, applicationID)
}
