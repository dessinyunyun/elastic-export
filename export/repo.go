package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/typedapi/core/closepointintime"
	"github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sortorder"
)

const batchSize = 500
const pitKeepAlive = "2m"

type Filter struct {
	ID             string
	ApplicationID  string
	Name           string
	Debt           *float64
	Address        string
	DecisionResult string
	CreatedAt      *time.Time
}

type summaryKey struct {
	ApplicationID string    `json:"applicationID"`
	CreatedAt     time.Time `json:"created_at"`
}

// RawMessage preserves detail fields and numeric values without float conversion.
type Row map[string]json.RawMessage

type ExportRepo struct {
	client       *elasticsearch.TypedClient
	summaryIndex string
	detailIndex  string
}

func NewExportRepo(client *elasticsearch.TypedClient, summaryIndex, detailIndex string) *ExportRepo {
	return &ExportRepo{client: client, summaryIndex: summaryIndex, detailIndex: detailIndex}
}

func (r *ExportRepo) openPIT(ctx context.Context) (string, error) {
	res, err := r.client.OpenPointInTime(r.summaryIndex + "," + r.detailIndex).KeepAlive(pitKeepAlive).AllowPartialSearchResults(false).Do(ctx)
	if err != nil {
		return "", fmt.Errorf("open export PIT: %w", err)
	}
	if res.Id == "" || res.Shards_.Failed > 0 {
		return res.Id, errors.New("open export PIT returned incomplete shards or empty ID")
	}
	return res.Id, nil
}

func (r *ExportRepo) closePIT(ctx context.Context, id string) error {
	res, err := r.client.ClosePointInTime().Request(&closepointintime.Request{Id: id}).Do(ctx)
	if err != nil {
		return err
	}
	if !res.Succeeded {
		return errors.New("Elasticsearch did not confirm PIT cleanup")
	}
	return nil
}

func (r *ExportRepo) searchPIT(ctx context.Context, pitID *string, request *search.Request) (*search.Response, error) {
	request.Pit = &types.PointInTimeReference{Id: *pitID, KeepAlive: pitKeepAlive}
	// PIT searches deliberately omit Index(): the PIT already identifies the indexes.
	res, err := r.client.Search().AllowPartialSearchResults(false).Request(request).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("export search: %w", err)
	}
	// Save the newest ID even when the response reports incomplete results.
	if res.PitId != nil && *res.PitId != "" {
		*pitID = *res.PitId
	}
	if res.TimedOut || res.Shards_.Failed > 0 {
		return nil, errors.New("export search returned incomplete results")
	}
	return res, nil
}

func (r *ExportRepo) summaryBatch(ctx context.Context, pitID *string, filter Filter, after []types.FieldValue) ([]summaryKey, []types.FieldValue, error) {
	filters := []types.Query{{Term: map[string]types.TermQuery{"_index": {Value: r.summaryIndex}}}}
	if filter.ID != "" {
		filters = append(filters, types.Query{Ids: &types.IdsQuery{Values: []string{filter.ID}}})
	}
	if filter.ApplicationID != "" {
		filters = append(filters, types.Query{Term: map[string]types.TermQuery{"applicationID": {Value: filter.ApplicationID}}})
	}
	if filter.Name != "" {
		filters = append(filters, types.Query{MatchPhrase: map[string]types.MatchPhraseQuery{"name": {Query: filter.Name}}})
	}
	if filter.Debt != nil {
		filters = append(filters, types.Query{Term: map[string]types.TermQuery{"debt": {Value: *filter.Debt}}})
	}
	if filter.Address != "" {
		filters = append(filters, types.Query{MatchPhrase: map[string]types.MatchPhraseQuery{"address": {Query: filter.Address}}})
	}
	if filter.DecisionResult != "" {
		filters = append(filters, types.Query{Term: map[string]types.TermQuery{"decision_result": {Value: filter.DecisionResult}}})
	}
	if filter.CreatedAt != nil {
		filters = append(filters, types.Query{Term: map[string]types.TermQuery{"created_at": {Value: filter.CreatedAt.UTC().Format(time.RFC3339Nano)}}})
	}
	query := types.Query{Bool: &types.BoolQuery{Filter: filters}}
	size := batchSize
	res, err := r.searchPIT(ctx, pitID, &search.Request{
		Query: &query, Size: &size, TrackTotalHits: false, SearchAfter: after,
		Sort: []types.SortCombinations{
			types.SortOptions{SortOptions: map[string]types.FieldSort{"created_at": {Order: &sortorder.Desc}}},
			types.SortOptions{SortOptions: map[string]types.FieldSort{"applicationID": {Order: &sortorder.Asc}}},
		},
		Source_: &types.SourceFilter{Includes: []string{"applicationID", "created_at"}},
	})
	if err != nil {
		return nil, nil, err
	}
	if len(res.Hits.Hits) == 0 {
		return nil, nil, nil
	}
	keys := make([]summaryKey, 0, len(res.Hits.Hits))
	for _, hit := range res.Hits.Hits {
		var key summaryKey
		if err := json.Unmarshal(hit.Source_, &key); err != nil {
			return nil, nil, fmt.Errorf("decode export summary: %w", err)
		}
		if key.ApplicationID == "" || key.CreatedAt.IsZero() {
			return nil, nil, errors.New("summary is missing applicationID or created_at")
		}
		key.CreatedAt = key.CreatedAt.UTC()
		keys = append(keys, key)
	}
	// Preserve the entire sort array, including Elasticsearch's implicit _shard_doc.
	cursor := res.Hits.Hits[len(res.Hits.Hits)-1].Sort
	if len(cursor) == 0 {
		return nil, nil, errors.New("export summary has no search_after sort values")
	}
	return keys, cursor, nil
}

func (r *ExportRepo) detailBatch(ctx context.Context, pitID *string, keys []summaryKey) ([]Row, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	clauses := make([]types.Query, 0, len(keys))
	positions := make(map[summaryKey]int, len(keys))
	for i, key := range keys {
		if _, exists := positions[key]; exists {
			return nil, fmt.Errorf("duplicate summary pair for %s", key.ApplicationID)
		}
		positions[key] = i
		clauses = append(clauses, types.Query{Bool: &types.BoolQuery{Filter: []types.Query{
			{Term: map[string]types.TermQuery{"applicationID": {Value: key.ApplicationID}}},
			{Term: map[string]types.TermQuery{"created_at": {Value: key.CreatedAt.Format(time.RFC3339Nano)}}},
		}}})
	}
	size := len(keys) + 1
	res, err := r.searchPIT(ctx, pitID, &search.Request{
		Query: &types.Query{Bool: &types.BoolQuery{
			Filter: []types.Query{{Term: map[string]types.TermQuery{"_index": {Value: r.detailIndex}}}},
			Should: clauses, MinimumShouldMatch: "1",
		}},
		Size: &size, TrackTotalHits: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Hits.Total == nil || res.Hits.Total.Value != int64(len(keys)) {
		return nil, fmt.Errorf("export detail matches are missing or duplicated for batch of %d summaries", len(keys))
	}
	// Retain only this batch, restoring the summary's sort order after the join.
	rows := make([]Row, len(keys))
	for _, hit := range res.Hits.Hits {
		var key summaryKey
		if err := json.Unmarshal(hit.Source_, &key); err != nil {
			return nil, fmt.Errorf("decode detail key: %w", err)
		}
		key.CreatedAt = key.CreatedAt.UTC()
		position, exists := positions[key]
		if !exists || rows[position] != nil {
			return nil, fmt.Errorf("unexpected or duplicate detail for %s", key.ApplicationID)
		}
		var row Row
		if err := json.Unmarshal(hit.Source_, &row); err != nil {
			return nil, fmt.Errorf("decode detail row: %w", err)
		}
		if hit.Id_ != nil {
			row["id"], _ = json.Marshal(*hit.Id_)
		}
		if _, exists := row["variables"]; !exists {
			row["variables"] = json.RawMessage("[]")
		}
		rows[position] = row
	}
	for i, row := range rows {
		if row == nil {
			return nil, fmt.Errorf("missing detail for %s", keys[i].ApplicationID)
		}
	}
	return rows, nil
}
