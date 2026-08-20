//go:build linux

package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/app"
	"github.com/AaronWangTT/blobfuse-health-exporter/internal/config"
)

func TestRunExportsBaselineGaugeAndShutsDownAfterExternalCancellation(t *testing.T) {
	requests := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		select {
		case requests <- struct{}{}:
		default:
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reportDirectory := t.TempDir()
	if err := os.Chmod(reportDirectory, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	reportPath := filepath.Join(reportDirectory, "monitor_"+strconv.Itoa(os.Getpid())+".json")
	if err := os.WriteFile(reportPath, []byte(`[
		{"BlobfuseStats":[{"componentName":"libfuse","value":{"OpenFileHandles":2}}]},
	`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings := config.Default()
	settings.ReportDirectory = reportDirectory
	settings.PID = os.Getpid()
	settings.OTLPEndpoint = server.URL + "/v1/metrics"
	settings.RescanInterval = 5 * time.Millisecond
	settings.InitializationTimeout = time.Second
	settings.SourceDrainTimeout = 10 * time.Millisecond
	settings.ExportInterval = 20 * time.Millisecond
	settings.ExportTimeout = 5 * time.Millisecond
	settings.ShutdownTimeout = time.Second

	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan struct {
		result app.Result
		err    error
	}, 1)
	go func() {
		result, err := app.Run(ctx, settings, "test-version")
		resultChannel <- struct {
			result app.Result
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-requests:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for periodic OTLP export")
	}
	select {
	case completed := <-resultChannel:
		if completed.err != nil {
			t.Fatalf("Run() error = %v", completed.err)
		}
		if completed.result.SourceEnded || completed.result.UncleanGenerations != 0 {
			t.Fatalf("Run() result = %#v, want external cancellation without source drain", completed.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not complete bounded shutdown")
	}
}
