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
