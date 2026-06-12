package logx

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactURLSecrets(t *testing.T) {
	input := "https://host/snapshot?X-Amz-Signature=secret&token=abc"
	got := Redact(input)
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") {
		t.Fatalf("Redact leaked secret: %s", got)
	}
}

func TestRedactCommonCloudSignatures(t *testing.T) {
	input := "https://host/snapshot?X-Goog-Signature=secret&sig=azure&safe=value"
	got := Redact(input)
	if strings.Contains(got, "secret") || strings.Contains(got, "azure") {
		t.Fatalf("Redact leaked cloud signature: %s", got)
	}
	if !strings.Contains(got, "safe=value") {
		t.Fatalf("Redact removed safe query value: %s", got)
	}
}

func TestRedactAuthorizationHeader(t *testing.T) {
	got := Redact("Authorization: Bearer secret-token")
	if strings.Contains(got, "secret-token") {
		t.Fatalf("Redact leaked authorization header: %s", got)
	}
}

func TestJSONLoggerRedactsFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "json")
	l.Info("source_resolved", Fields{"url": "https://h/x?AWSAccessKeyId=secret"})
	out := buf.String()
	if !strings.Contains(out, `"event":"source_resolved"`) {
		t.Fatalf("missing event: %s", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("log leaked secret: %s", out)
	}
}

func TestTextLoggerUsesMessageField(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "text")
	l.Info("restore_progress", Fields{"message": "20.0% downloaded, 1.00 MiB/s, ETA 8s"})
	out := buf.String()
	if !strings.Contains(out, "INFO restore_progress 20.0% downloaded, 1.00 MiB/s, ETA 8s") {
		t.Fatalf("text log did not use message field: %s", out)
	}
	if strings.Contains(out, "map[") {
		t.Fatalf("text log included raw map: %s", out)
	}
}
