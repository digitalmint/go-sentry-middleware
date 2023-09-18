package sentry

import "testing"

func TestRedactDSN(t *testing.T) {
	result := RedactDSN([]byte(`{"a":"b", "dsn":"https://abc@def.ingest.sentry.io/123"}`))
	if string(result) != `{"a":"b", "dsn":"REDACTED"}` {
		t.Errorf("Redact test failed")
	}
}
