package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Fields map[string]any

type Logger struct {
	out    io.Writer
	format string
}

func New(out io.Writer, format string) *Logger {
	if format != "text" {
		format = "json"
	}
	return &Logger{out: out, format: format}
}

func (l *Logger) Info(event string, fields Fields) {
	l.write("info", event, fields)
}

func (l *Logger) Warn(event string, fields Fields) {
	l.write("warn", event, fields)
}

func (l *Logger) Error(event string, fields Fields) {
	l.write("error", event, fields)
}

func (l *Logger) write(level, event string, fields Fields) {
	if fields == nil {
		fields = Fields{}
	}
	if l.format == "text" {
		fields = redactFields(fields)
		if message, ok := fields["message"].(string); ok && message != "" {
			fmt.Fprintf(l.out, "%s %s %s %s\n", time.Now().UTC().Format(time.RFC3339), strings.ToUpper(level), event, message)
			return
		}
		fmt.Fprintf(l.out, "%s %s %s %v\n", time.Now().UTC().Format(time.RFC3339), strings.ToUpper(level), event, fields)
		return
	}
	rec := Fields{
		"ts":    time.Now().UTC().Format(time.RFC3339),
		"level": level,
		"event": event,
	}
	for k, v := range redactFields(fields) {
		rec[k] = v
	}
	_ = json.NewEncoder(l.out).Encode(rec)
}

func redactFields(in Fields) Fields {
	out := Fields{}
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = Redact(s)
		} else {
			out[k] = v
		}
	}
	return out
}

var secretQuery = map[string]bool{
	"x-amz-signature":      true,
	"x-amz-credential":     true,
	"x-amz-security-token": true,
	"awsaccesskeyid":       true,
	"signature":            true,
	"token":                true,
	"access_token":         true,
}

var authHeaderRE = regexp.MustCompile(`(?i)(authorization:\s*)\S+(\s+\S+)?`)

func Redact(s string) string {
	s = authHeaderRE.ReplaceAllString(s, `${1}[REDACTED]`)
	parts := strings.Fields(s)
	for i, part := range parts {
		if u, err := url.Parse(part); err == nil && u.Scheme != "" && u.Host != "" {
			q := u.Query()
			changed := false
			for key := range q {
				if secretQuery[strings.ToLower(key)] {
					q.Set(key, "[REDACTED]")
					changed = true
				}
			}
			if changed {
				u.RawQuery = q.Encode()
				parts[i] = u.String()
			}
		}
	}
	return strings.Join(parts, " ")
}
