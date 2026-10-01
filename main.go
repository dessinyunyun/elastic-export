package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"elastic-testing/export"

	"github.com/elastic/go-elasticsearch/v9"
)

func main() {
	client, err := elasticsearch.NewTypedClient(elasticsearch.Config{Addresses: []string{env("ELASTICSEARCH_URL", "http://localhost:9200")}})
	if err != nil {
		log.Fatal(err)
	}
	manager := &IndexManager{client: client}
	repo := &EngineDetailRepo{client: client, index: detailAlias}
	summaryRepo := &EngineSummaryRepo{client: client, index: summaryAlias}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = manager.Ensure(ctx)
	cancel()
	if err != nil {
		log.Fatalf("prepare Elasticsearch: %v", err)
	}

	service := NewEngineDetailService(repo)
	handler := NewEngineDetailHandler(service)
	summaryService := NewEngineSummaryService(summaryRepo)
	summaryHandler := NewEngineSummaryHandler(summaryService)
	dummyHandler := NewDummyHandler(manager)
	exportRepo := export.NewExportRepo(client, summaryAlias, detailAlias)
	exportService := export.NewExportService(exportRepo)
	exportHandler := export.NewExportHandler(exportService)
	server := &http.Server{Addr: env("HTTP_ADDR", "127.0.0.1:8080"), Handler: NewRoutes(handler, summaryHandler, dummyHandler, exportHandler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: dummyTimeout + 30*time.Second, IdleTimeout: 60 * time.Second}
	stop, cancelStop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancelStop()
	go manager.Monitor(stop)
	go func() {
		<-stop.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("API running at http://%s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
