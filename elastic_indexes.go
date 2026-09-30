package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v9"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
)

const (
	detailAlias       = "engine-detail"
	summaryAlias      = "engine-summary"
	maxIndexDocuments = 30000
)

var versionedIndex = regexp.MustCompile(`^(engine-detail-([0-9]{4,})|engine-summary-v([0-9]{3,}))$`)

// One manager is shared by startup, the rollover worker, and the dummy writer.
// Run a single Go instance as the lifecycle owner for these two index families.
type IndexManager struct {
	client *elasticsearch.TypedClient
	mu     sync.Mutex
}

func engineMapping(detail bool) *types.TypeMapping {
	properties := map[string]types.Property{
		"applicationID":   types.NewKeywordProperty(),
		"name":            types.NewTextProperty(),
		"debt":            types.NewDoubleNumberProperty(),
		"address":         types.NewTextProperty(),
		"decision_result": types.NewKeywordProperty(),
		"created_at":      types.NewDateProperty(),
	}
	if detail {
		variables := types.NewNestedProperty()
		variables.Properties = map[string]types.Property{
			"age":     types.NewIntegerNumberProperty(),
			"married": types.NewBooleanProperty(),
			"salary":  types.NewDoubleNumberProperty(),
		}
		properties["variables"] = variables
	}
	return &types.TypeMapping{Properties: properties}
}

func versionName(alias string, version int) string {
	if alias == detailAlias {
		return fmt.Sprintf("engine-detail-%04d", version)
	}
	return fmt.Sprintf("engine-summary-v%03d", version)
}

func indexVersion(name string) (string, int) {
	match := versionedIndex.FindStringSubmatch(name)
	if match == nil {
		return "", 0
	}
	alias, digits := detailAlias, match[2]
	if digits == "" {
		alias, digits = summaryAlias, match[3]
	}
	version, _ := strconv.Atoi(digits)
	return alias, version
}

// request reuses the official client's transport for administrative APIs.
func (m *IndexManager) request(ctx context.Context, method, path string, body any, out any) error {
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, path, &payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := m.client.Perform(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
		return fmt.Errorf("Elasticsearch %s %s: HTTP %d: %s", method, path, res.StatusCode, message)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	_, err = io.Copy(io.Discard, res.Body)
	return err
}

func (m *IndexManager) concreteIndexes(ctx context.Context) ([]string, error) {
	var rows []struct {
		Index string `json:"index"`
	}
	if err := m.request(ctx, "GET", "/_cat/indices?format=json&h=index", nil, &rows); err != nil {
		return nil, err
	}
	names := []string{}
	for _, row := range rows {
		if alias, _ := indexVersion(row.Index); alias != "" || row.Index == detailAlias || row.Index == summaryAlias {
			names = append(names, row.Index)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, av := indexVersion(names[i])
		b, bv := indexVersion(names[j])
		if a == b {
			return av < bv
		}
		return a < b
	})
	return names, nil
}

func (m *IndexManager) installTemplates(ctx context.Context) error {
	for _, alias := range []string{detailAlias, summaryAlias} {
		pattern := "engine-detail-*"
		if alias == summaryAlias {
			pattern = "engine-summary-v*"
		}
		body := map[string]any{
			"index_patterns": []string{pattern}, "priority": 200,
			"template": map[string]any{
				"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
				"mappings": engineMapping(alias == detailAlias),
			},
		}
		if err := m.request(ctx, "PUT", "/_index_template/"+alias+"-versions", body, nil); err != nil {
			return err
		}
	}
	return nil
}

func (m *IndexManager) Ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.installTemplates(ctx); err != nil {
		return err
	}
	return m.syncAliases(ctx)
}

// syncAliases discovers concrete versions and atomically assigns the newest as
// the sole write index. Older versions remain members of the read alias.
func (m *IndexManager) syncAliases(ctx context.Context) error {
	names, err := m.concreteIndexes(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == detailAlias || name == summaryAlias {
			return fmt.Errorf("legacy concrete index %s blocks alias creation; follow the explicit reset in elastic-query.md", name)
		}
	}
	for _, alias := range []string{detailAlias, summaryAlias} {
		versions := []string{}
		for _, name := range names {
			if family, _ := indexVersion(name); family == alias {
				versions = append(versions, name)
			}
		}
		if len(versions) == 0 {
			name := versionName(alias, 1)
			if err := m.request(ctx, "PUT", "/"+name, map[string]any{}, nil); err != nil {
				return err
			}
			versions = append(versions, name)
		}
		actions := []any{}
		for i, name := range versions {
			actions = append(actions, map[string]any{"add": map[string]any{
				"index": name, "alias": alias, "is_write_index": i == len(versions)-1,
			}})
		}
		if err := m.request(ctx, "POST", "/_aliases", map[string]any{"actions": actions}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (m *IndexManager) writeIndex(ctx context.Context, alias string) (string, error) {
	var entries map[string]struct {
		Aliases map[string]struct {
			IsWriteIndex bool `json:"is_write_index"`
		} `json:"aliases"`
	}
	if err := m.request(ctx, "GET", "/_alias/"+alias, nil, &entries); err != nil {
		return "", err
	}
	for index, entry := range entries {
		if entry.Aliases[alias].IsWriteIndex {
			return index, nil
		}
	}
	return "", fmt.Errorf("alias %s has no write index", alias)
}

// capacity requires mu held. Count uses root documents, excluding nested variables.
func (m *IndexManager) capacity(ctx context.Context, alias string) (int, error) {
	index, err := m.writeIndex(ctx, alias)
	if err != nil {
		return 0, err
	}
	if err := m.request(ctx, "POST", "/"+index+"/_refresh", nil, nil); err != nil {
		return 0, err
	}
	count, err := m.client.Count().Index(index).Do(ctx)
	if err != nil {
		return 0, err
	}
	if count.Shards_.Failed > 0 {
		return 0, fmt.Errorf("count failed for %s", index)
	}
	if count.Count < maxIndexDocuments {
		return maxIndexDocuments - int(count.Count), nil
	}
	family, version := indexVersion(index)
	if family != alias {
		return 0, fmt.Errorf("unexpected write index %s for %s", index, alias)
	}
	next := versionName(alias, version+1)
	var result struct {
		RolledOver bool `json:"rolled_over"`
	}
	if err := m.request(ctx, "POST", "/"+alias+"/_rollover/"+next,
		map[string]any{"conditions": map[string]any{"max_docs": maxIndexDocuments}}, &result); err != nil {
		return 0, err
	}
	if !result.RolledOver {
		return 0, fmt.Errorf("rollover of %s did not complete", alias)
	}
	log.Printf("rollover: %s write index %s -> %s", alias, index, next)
	return maxIndexDocuments, nil
}

func (m *IndexManager) Monitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !m.mu.TryLock() {
				continue
			}
			checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := m.syncAliases(checkCtx)
			if err == nil {
				for _, alias := range []string{detailAlias, summaryAlias} {
					if _, err = m.capacity(checkCtx, alias); err != nil {
						break
					}
				}
			}
			cancel()
			m.mu.Unlock()
			if err != nil {
				log.Printf("index lifecycle: %v", err)
			}
		}
	}
}

// ResetDummy is deliberately destructive and is only invoked by POST /dummy.
// It removes only exact legacy names and strict, versioned members of our families.
func (m *IndexManager) ResetDummy(ctx context.Context) error {
	names, err := m.concreteIndexes(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := m.request(ctx, "DELETE", "/"+name, nil, nil); err != nil {
			return err
		}
	}
	if err := m.installTemplates(ctx); err != nil {
		return err
	}
	return m.syncAliases(ctx)
}
