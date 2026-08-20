//go:build linux

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunPrintsHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "--report-dir") || !strings.Contains(stdout.String(), "--otlp-endpoint") {
		t.Fatalf("help output = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsInvalidConfigurationWithoutEchoingSecret(t *testing.T) {
	const secret = "do-not-print-this"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--report-dir=/reports",
		"--pid=1234",
		"--otlp-endpoint=http://user:" + secret + "@localhost/v1/metrics",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr exposed endpoint secret: %q", stderr.String())
	}
}

func TestLogLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if got := logLevel(level).String(); got != strings.ToUpper(level) {
			t.Fatalf("logLevel(%q) = %q", level, got)
		}
	}
}
