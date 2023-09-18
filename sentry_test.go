package sentry

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestRedactDSN(t *testing.T) {
	result := RedactDSN([]byte(`{"a":"b", "dsn":"https://abc@def.ingest.sentry.io/123"}`))
	if string(result) != `{"a":"b", "dsn":"REDACTED"}` {
		t.Errorf("Redact test failed")
	}
}

type roundTripFunc func(r *http.Request) (*http.Response, error)

func (s roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return s(r)
}

func TestRoundTrip(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello, client")
		w.WriteHeader(400)
	}))
	defer ts.Close()

	client := ts.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		lsf := LogSentrySendFailures{Logger: zap.S(), RT: transport}
		return lsf.RoundTrip(r)
	})
	res, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	greeting, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	if string(greeting) != "Hello, client" {
		t.Fatal("not greeted", string(greeting))
	}
}
