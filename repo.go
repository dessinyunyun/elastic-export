package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
)

type DecisionResult string

const (
	DecisionReject  DecisionResult = "reject"
	DecisionApprove DecisionResult = "approve"
)

type EngineDetail struct {
	ID             string           `json:"id"`
	ApplicationID  string           `json:"applicationID"`
	Name           string           `json:"name"`
	Debt           float64          `json:"debt"`
	Address        string           `json:"address"`
	DecisionResult DecisionResult   `json:"decision_result"`
	CreatedAt      time.Time        `json:"created_at"`
	Variables      []map[string]any `json:"variables"`
}

type EngineDetailRepo struct {
	client *elasticsearch.TypedClient
	index  string
}

func (r *EngineDetailRepo) Health(ctx context.Context) error {
	_, err := r.client.Info().Do(ctx)
	return err
}

func (r *EngineDetailRepo) GetByApplicationID(ctx context.Context, applicationID string) ([]EngineDetail, error) {
	query := types.Query{Term: map[string]types.TermQuery{
		"applicationID": {Value: applicationID},
	}}
	return r.searchByQuery(ctx, query)
}

func (r *EngineDetailRepo) Search(ctx context.Context, term, applicationID string) ([]EngineDetail, error) {
	query := types.Query{MatchAll: &types.MatchAllQuery{}}
	if term != "" {
		query = types.Query{MultiMatch: &types.MultiMatchQuery{Query: term, Fields: []string{"name", "address"}}}
	}
	if applicationID != "" {
		query = types.Query{Bool: &types.BoolQuery{
			Must:   []types.Query{query},
			Filter: []types.Query{{Term: map[string]types.TermQuery{"applicationID": {Value: applicationID}}}},
		}}
	}
	return r.searchByQuery(ctx, query)
}

func (r *EngineDetailRepo) searchByQuery(ctx context.Context, query types.Query) ([]EngineDetail, error) {
	size := 100
	res, err := r.client.Search().Index(r.index).Request(&search.Request{Query: &query, Size: &size}).Do(ctx)
	if err != nil {
		return nil, err
	}
	details := make([]EngineDetail, 0, len(res.Hits.Hits))
	for _, hit := range res.Hits.Hits {
		var detail EngineDetail
		if err := json.Unmarshal(hit.Source_, &detail); err != nil {
			return nil, fmt.Errorf("decode engine detail: %w", err)
		}
		if hit.Id_ != nil {
			detail.ID = *hit.Id_
		}
		if detail.Variables == nil {
			detail.Variables = make([]map[string]any, 0)
		}
		details = append(details, detail)
	}
	return details, nil
}

func isMissing(err error) bool {
	var esErr *types.ElasticsearchError
	return errors.As(err, &esErr) && esErr.Status == 404
}
