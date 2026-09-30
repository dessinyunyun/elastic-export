package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v9"
	"github.com/gin-gonic/gin"
)

const (
	dummyApplications = 20000
	dummyBatchSize    = 500
	dummyTimeout      = 5 * time.Minute
)

var errDummyRunning = errors.New("dummy generation is already running")

// The JSON source intentionally excludes id: document IDs live in Elasticsearch _id.
// Summary and detail use this same payload; only detail has Variables populated.
type dummyDocument struct {
	ApplicationID  string           `json:"applicationID"`
	Name           string           `json:"name"`
	Debt           float64          `json:"debt"`
	Address        string           `json:"address"`
	DecisionResult DecisionResult   `json:"decision_result"`
	CreatedAt      time.Time        `json:"created_at"`
	Variables      []map[string]any `json:"variables,omitempty"`
}

type dummyWrite struct {
	id     string
	source dummyDocument
}

type DummyResult struct {
	Applications      int              `json:"applications"`
	DetailDocuments   int64            `json:"detail_documents"`
	SummaryDocuments  int64            `json:"summary_documents"`
	DetailWriteIndex  string           `json:"detail_write_index"`
	SummaryWriteIndex string           `json:"summary_write_index"`
	IndexDocuments    map[string]int64 `json:"index_documents"`
}

type DummyRepo struct {
	client       *elasticsearch.TypedClient
	detailIndex  string
	summaryIndex string
	manager      *IndexManager
}

type DummyService struct {
	repo *DummyRepo
	mu   sync.Mutex
}

type DummyHandler struct {
	service *DummyService
}

func NewDummyHandler(manager *IndexManager) *DummyHandler {
	return &DummyHandler{service: &DummyService{repo: &DummyRepo{
		client: manager.client, detailIndex: detailAlias, summaryIndex: summaryAlias, manager: manager,
	}}}
}

func (h *DummyHandler) Generate(c *gin.Context) {
	result, err := h.service.Generate(c.Request.Context())
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errDummyRunning) {
			status = http.StatusConflict
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		WriteError(c, status, err.Error())
		return
	}
	WriteResponse(c, http.StatusOK, result)
}

// Generate rebuilds the dataset. A failed run may leave partial data; rerunning
// starts clean. The lock prevents concurrent generators in this server process.
func (s *DummyService) Generate(ctx context.Context) (*DummyResult, error) {
	if !s.mu.TryLock() {
		return nil, errDummyRunning
	}
	defer s.mu.Unlock()
	s.repo.manager.mu.Lock()
	defer s.repo.manager.mu.Unlock()
	if err := s.repo.Reset(ctx); err != nil {
		return nil, err
	}

	firstNames := []string{"Budi", "Siti", "Andi", "Dewi", "Rizki", "Putri", "Agus", "Rina"}
	lastNames := []string{"Santoso", "Wijaya", "Pratama", "Lestari", "Saputra", "Permata"}
	cities := []string{"Jakarta", "Bandung", "Surabaya", "Semarang", "Medan", "Makassar"}
	// Four ordered rounds give every application four histories. Across the
	// entire dataset, created_at increases by one second for each inserted row.
	baseTime := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	batch := make([]dummyWrite, 0, dummyBatchSize)
	summaries := make([]dummyWrite, dummyApplications)
	detailCount := 0
	for occurrence := 1; occurrence <= 4; occurrence++ {
		for app := 1; app <= dummyApplications; app++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			applicationID := fmt.Sprintf("APP-%06d", app)
			decision := DecisionReject
			if rand.IntN(2) == 1 {
				decision = DecisionApprove
			}
			doc := dummyDocument{
				ApplicationID:  applicationID,
				Name:           firstNames[(app-1)%len(firstNames)] + " " + lastNames[(app-1)%len(lastNames)],
				Address:        fmt.Sprintf("Jl. Melati No. %d, %s", 1+app%200, cities[(app-1)%len(cities)]),
				Debt:           float64(5+rand.IntN(196)) * 500000,
				DecisionResult: decision,
				CreatedAt:      baseTime.Add(time.Duration(detailCount) * time.Second),
				Variables:      []map[string]any{{"age": 21 + app%40, "married": app%2 == 0, "salary": float64(3+rand.IntN(58)) * 500000}},
			}
			batch = append(batch, dummyWrite{id: fmt.Sprintf("%s-%02d", applicationID, occurrence), source: doc})
			detailCount++
			// Only the shared fields are copied; variables remains detail-only.
			latest := doc
			latest.Variables = nil
			summaries[app-1] = dummyWrite{id: applicationID, source: latest}
			if len(batch) == dummyBatchSize {
				if err := s.repo.WriteBatch(ctx, s.repo.detailIndex, batch); err != nil {
					return nil, err
				}
				batch = batch[:0]
			}
		}
	}
	if len(batch) > 0 {
		if err := s.repo.WriteBatch(ctx, s.repo.detailIndex, batch); err != nil {
			return nil, err
		}
	}
	// All detail writes must succeed before any summaries are published.
	for start := 0; start < len(summaries); start += dummyBatchSize {
		end := min(start+dummyBatchSize, len(summaries))
		if err := s.repo.WriteBatch(ctx, s.repo.summaryIndex, summaries[start:end]); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Refresh(ctx); err != nil {
		return nil, err
	}
	result := &DummyResult{Applications: dummyApplications}
	var err error
	result.DetailDocuments, err = s.repo.Count(ctx, s.repo.detailIndex)
	if err != nil {
		return nil, err
	}
	result.SummaryDocuments, err = s.repo.Count(ctx, s.repo.summaryIndex)
	if err != nil {
		return nil, err
	}
	if result.DetailDocuments != int64(detailCount) || result.DetailDocuments <= dummyApplications || result.SummaryDocuments != dummyApplications {
		return nil, fmt.Errorf("unexpected counts: details=%d (expected %d), summaries=%d (expected %d); do not write to these indexes during generation", result.DetailDocuments, detailCount, result.SummaryDocuments, dummyApplications)
	}
	result.DetailWriteIndex, err = s.repo.manager.writeIndex(ctx, detailAlias)
	if err != nil {
		return nil, err
	}
	result.SummaryWriteIndex, err = s.repo.manager.writeIndex(ctx, summaryAlias)
	if err != nil {
		return nil, err
	}
	names, err := s.repo.manager.concreteIndexes(ctx)
	if err != nil {
		return nil, err
	}
	result.IndexDocuments = make(map[string]int64, len(names))
	for _, name := range names {
		result.IndexDocuments[name], err = s.repo.Count(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Reset rebuilds the versioned dummy indexes while the manager lock is held.
func (r *DummyRepo) Reset(ctx context.Context) error {
	return r.manager.ResetDummy(ctx)
}

func (r *DummyRepo) WriteBatch(ctx context.Context, alias string, docs []dummyWrite) error {
	for len(docs) > 0 {
		capacity, err := r.manager.capacity(ctx, alias)
		if err != nil {
			return err
		}
		n := min(len(docs), capacity, dummyBatchSize)
		if err := r.writeBulk(ctx, alias, docs[:n]); err != nil {
			return err
		}
		docs = docs[n:]
	}
	// Move the write alias immediately when a version reaches 30,000 rows.
	_, err := r.manager.capacity(ctx, alias)
	return err
}
func (r *DummyRepo) writeBulk(ctx context.Context, index string, docs []dummyWrite) error {
	if len(docs) == 0 {
		return nil
	}
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, doc := range docs {
		if err := encoder.Encode(map[string]any{"index": map[string]string{"_id": doc.id}}); err != nil {
			return fmt.Errorf("encode bulk metadata: %w", err)
		}
		if err := encoder.Encode(doc.source); err != nil {
			return fmt.Errorf("encode bulk document: %w", err)
		}
	}
	res, err := r.client.Bulk().Index(index).Raw(&payload).Do(ctx)
	if err != nil {
		return fmt.Errorf("bulk %s: %w", index, err)
	}
	if len(res.Items) != len(docs) {
		return fmt.Errorf("bulk %s returned %d items for %d documents", index, len(res.Items), len(docs))
	}
	failed := 0
	var firstFailure string
	for i, item := range res.Items {
		if len(item) != 1 {
			return fmt.Errorf("bulk %s: invalid response item for %s", index, docs[i].id)
		}
		for _, outcome := range item {
			if outcome.Status < 200 || outcome.Status >= 300 || outcome.Error != nil {
				failed++
				if firstFailure == "" {
					detail, _ := json.Marshal(outcome.Error)
					firstFailure = fmt.Sprintf("id=%s status=%d error=%s", docs[i].id, outcome.Status, detail)
				}
			}
		}
	}
	if failed > 0 || res.Errors {
		return fmt.Errorf("bulk %s failed (%d items): %s; dataset may be partial, rerun POST /dummy", index, failed, firstFailure)
	}
	return nil
}

func (r *DummyRepo) Refresh(ctx context.Context) error {
	res, err := r.client.Indices.Refresh().Index(r.detailIndex + "," + r.summaryIndex).Do(ctx)
	if err != nil {
		return fmt.Errorf("refresh dummy indexes: %w", err)
	}
	if res.Shards_.Failed > 0 {
		return errors.New("refresh dummy indexes: some shards failed")
	}
	return nil
}

func (r *DummyRepo) Count(ctx context.Context, index string) (int64, error) {
	res, err := r.client.Count().Index(index).Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", index, err)
	}
	if res.Shards_.Failed > 0 {
		return 0, fmt.Errorf("count %s: some shards failed", index)
	}
	return res.Count, nil
}
