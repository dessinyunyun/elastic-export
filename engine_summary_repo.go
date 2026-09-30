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
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sortorder"
)

var ErrSummaryNotFound = errors.New("engine summary not found")

type EngineSummary struct {
	ID             string         `json:"id"`
	ApplicationID  string         `json:"applicationID"`
	Name           string         `json:"name"`
	Debt           float64        `json:"debt"`
	Address        string         `json:"address"`
	DecisionResult DecisionResult `json:"decision_result"`
	CreatedAt      time.Time      `json:"created_at"`
}

type EngineSummaryRepo struct {
	client *elasticsearch.TypedClient
	index  string
}

func (r *EngineSummaryRepo) Get(ctx context.Context, id string) (*EngineSummary, error) {
	size := 2
	res, err := r.executeSearch(ctx, &search.Request{
		Query: &types.Query{Ids: &types.IdsQuery{Values: []string{id}}},
		Size:  &size, TrackTotalHits: true,
	})
	if isMissing(err) {
		return nil, ErrSummaryNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(res.Hits.Hits) == 0 {
		return nil, ErrSummaryNotFound
	}
	if res.Hits.Total.Value != 1 {
		return nil, fmt.Errorf("duplicate summary id %s across index versions", id)
	}
	hit := res.Hits.Hits[0]
	var summary EngineSummary
	if err := json.Unmarshal(hit.Source_, &summary); err != nil {
		return nil, fmt.Errorf("decode engine summary: %w", err)
	}
	summary.ID = id
	return &summary, nil
}

func (r *EngineSummaryRepo) Search(ctx context.Context, term, applicationID string, pagination Pagination) ([]EngineSummary, int64, error) {
	if err := pagination.Validate(); err != nil {
		return nil, 0, err
	}
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
	res, err := r.searchPage(ctx, query, pagination)
	if err != nil {
		return nil, 0, err
	}
	summaries := make([]EngineSummary, 0, len(res.Hits.Hits))
	for _, hit := range res.Hits.Hits {
		var summary EngineSummary
		if err := json.Unmarshal(hit.Source_, &summary); err != nil {
			return nil, 0, fmt.Errorf("decode engine summary: %w", err)
		}
		if hit.Id_ != nil {
			summary.ID = *hit.Id_
		}
		summaries = append(summaries, summary)
	}
	return summaries, res.Hits.Total.Value, nil
}

func (r *EngineSummaryRepo) searchPage(ctx context.Context, query types.Query, pagination Pagination) (*search.Response, error) {
	offset, size := pagination.Offset(), pagination.Limit
	request := &search.Request{
		Query: &query, From: &offset, Size: &size, TrackTotalHits: true,
		// Summary has exactly one document per applicationID, giving a stable order.
		Sort: []types.SortCombinations{types.SortOptions{SortOptions: map[string]types.FieldSort{
			"applicationID": {Order: &sortorder.Asc},
		}}},
	}
	if offset+size > 10000 {
		// Avoid max_result_window without fetching/deserializing all document sources.
		// Deep page jumps walk sort values in bounded Elasticsearch requests.
		request.From = nil
		request.Source_ = false
		remaining := offset
		for remaining > 0 {
			step := min(remaining, 1000)
			request.Size = &step
			res, err := r.executeSearch(ctx, request)
			if err != nil {
				return nil, err
			}
			if int64(offset) >= res.Hits.Total.Value || len(res.Hits.Hits) == 0 {
				res.Hits.Hits = nil
				return res, nil
			}
			remaining -= len(res.Hits.Hits)
			request.SearchAfter = res.Hits.Hits[len(res.Hits.Hits)-1].Sort
			if len(request.SearchAfter) == 0 {
				return nil, errors.New("Elasticsearch did not return pagination sort values")
			}
		}
		request.Source_ = true
		request.Size = &size
	}
	return r.executeSearch(ctx, request)
}

func (r *EngineSummaryRepo) executeSearch(ctx context.Context, request *search.Request) (*search.Response, error) {
	res, err := r.client.Search().Index(r.index).Request(request).Do(ctx)
	if err != nil {
		return nil, err
	}
	if res.TimedOut || res.Shards_.Failed > 0 {
		return nil, errors.New("Elasticsearch returned incomplete search results")
	}
	if res.Hits.Total == nil {
		return nil, errors.New("Elasticsearch did not return an exact total")
	}
	return res, nil
}
