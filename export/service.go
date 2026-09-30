package export

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
)

type ExportService struct{ repo *ExportRepo }

var ErrInvalidFilter = errors.New("invalid export filter")

func NewExportService(repo *ExportRepo) *ExportService { return &ExportService{repo: repo} }

// Export retains at most one summary/detail batch and waits for the consumer
// before issuing the next query. This provides backpressure for slow clients.
func (s *ExportService) Export(ctx context.Context, filter Filter, consume func([]Row) error) error {
	filter.ID = strings.TrimSpace(filter.ID)
	filter.ApplicationID = strings.TrimSpace(filter.ApplicationID)
	filter.Name = strings.TrimSpace(filter.Name)
	filter.Address = strings.TrimSpace(filter.Address)
	filter.DecisionResult = strings.TrimSpace(filter.DecisionResult)
	if filter.DecisionResult != "" && filter.DecisionResult != "approve" && filter.DecisionResult != "reject" {
		return fmt.Errorf("%w: decision_result must be approve or reject", ErrInvalidFilter)
	}
	if filter.Debt != nil && (math.IsNaN(*filter.Debt) || math.IsInf(*filter.Debt, 0) || *filter.Debt < 0) {
		return fmt.Errorf("%w: debt must be a finite, non-negative number", ErrInvalidFilter)
	}
	var pitID string
	defer func() {
		if pitID == "" {
			return
		}
		// Close the latest shared PIT ID even if the request was canceled.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.repo.closePIT(cleanupCtx, pitID); err != nil {
			log.Printf("export PIT cleanup: %v", err)
		}
	}()
	var err error
	pitID, err = s.repo.openPIT(ctx)
	if err != nil {
		return err
	}
	var after []types.FieldValue
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		keys, next, err := s.repo.summaryBatch(ctx, &pitID, filter, after)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := s.repo.detailBatch(ctx, &pitID, keys)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := consume(rows); err != nil {
			return err
		}
		after = next
	}
}
