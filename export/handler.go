package export

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const RequestTimeout = 30 * time.Minute
const writeTimeout = 30 * time.Second

type ExportHandler struct {
	service *ExportService
}

func NewExportHandler(service *ExportService) *ExportHandler {
	return &ExportHandler{service: service}
}

func (h *ExportHandler) Stream(c *gin.Context) error {
	filter, err := parseFilter(c)
	if err != nil {
		return err
	}
	ctx := c.Request.Context()
	var responseWriter http.ResponseWriter = c.Writer
	if unwrapper, ok := responseWriter.(interface{ Unwrap() http.ResponseWriter }); ok {
		responseWriter = unwrapper.Unwrap()
	}
	controller := http.NewResponseController(responseWriter)
	// Unblock a pending network write promptly when the request is canceled.
	cancelWriteDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		close(cancelWriteDone)
	})
	defer func() {
		if !stop() {
			<-cancelWriteDone
		}
	}()
	flushHTTP := func() error {
		c.Writer.WriteHeaderNow()
		return controller.Flush()
	}
	buffer := bufio.NewWriterSize(c.Writer, 64*1024)
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	started := false
	var count int64
	prepareWrite := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline := time.Now().Add(writeTimeout)
		if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
			deadline = requestDeadline
		}
		return controller.SetWriteDeadline(deadline)
	}
	begin := func() {
		c.Header("Content-Type", "application/x-ndjson")
		c.Header("Content-Disposition", `attachment; filename="engine-export.ndjson"`)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
		started = true
	}
	err = h.service.Export(ctx, filter, func(rows []Row) error {
		if err := prepareWrite(); err != nil {
			return err
		}
		if !started {
			begin()
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := encoder.Encode(row); err != nil {
				return err
			}
			count++
		}
		if err := buffer.Flush(); err != nil {
			return err
		}
		return flushHTTP()
	})
	if err != nil {
		log.Printf("export stopped after %d rows (stream_started=%t): %v", count, started, err)
		// Do not flush leftover bytes or append a JSON error to a partial NDJSON stream.
		return err
	}
	if !started {
		if err := prepareWrite(); err != nil {
			return err
		}
		begin()
		if err := flushHTTP(); err != nil {
			log.Printf("empty export flush: %v", err)
			return err
		}
	}
	log.Printf("export completed: %d rows", count)
	return nil
}

func parseFilter(c *gin.Context) (Filter, error) {
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return Filter{}, fmt.Errorf("%w: malformed query parameters", ErrInvalidFilter)
	}
	for name, values := range params {
		switch name {
		case "id", "applicationID", "name", "debt", "address", "decision_result", "created_at":
		default:
			return Filter{}, fmt.Errorf("%w: unsupported parameter %q; use individual summary fields", ErrInvalidFilter, name)
		}
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return Filter{}, fmt.Errorf("%w: %s must have one non-empty value", ErrInvalidFilter, name)
		}
	}
	filter := Filter{
		ID: params.Get("id"), ApplicationID: params.Get("applicationID"),
		Name: params.Get("name"), Address: params.Get("address"),
		DecisionResult: params.Get("decision_result"),
	}
	if value, exists := params["debt"]; exists {
		debt, parseErr := strconv.ParseFloat(strings.TrimSpace(value[0]), 64)
		if parseErr != nil {
			return Filter{}, fmt.Errorf("%w: debt must be a number", ErrInvalidFilter)
		}
		filter.Debt = &debt
	}
	if value, exists := params["created_at"]; exists {
		createdAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(value[0]))
		if parseErr != nil {
			return Filter{}, fmt.Errorf("%w: created_at must be RFC3339, for example 2026-09-28T02:24:04Z", ErrInvalidFilter)
		}
		filter.CreatedAt = &createdAt
	}
	return filter, nil
}
