package sentry

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/getsentry/sentry-go"
	"gitlab.com/digitalmint/bravo/lib"
	"go.uber.org/zap"
)

type SentryError500 struct {
	Url  string
	Body string
}

func (e500 SentryError500) Error() string {
	return "500 " + e500.Url + ":" + e500.Body
}

// group on the url and the beginning of the body.
// The same url can have different errors: thus looking at the response body.
// the longer the body is, the more likely it is to contain variable
// So for now try looking at a beginning snippet of the body.
// The URL is normalized so that any path part with a number is replaced by a placeholder value
func SentryFingerprint(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	if oe := hint.OriginalException; oe != nil {
		//nolint:errorlint
		if ex, ok := oe.(SentryError500); ok {
			message := ex.Body
			if len(ex.Body) > 15 {
				message = ex.Body[0:15]
			}
			newPath, err := lib.NormalizeUrlPathForSentry(ex.Url, "")
			if err != nil {
				zap.S().Error(err)
			}

			event.Fingerprint = []string{newPath, message}
			return event
		}
	}
	return event
}

func HubCustomFingerprint(hub *sentry.Hub) *sentry.Hub {
	clientOld, scope := hub.Client(), hub.Scope()
	options := sentry.ClientOptions{}
	if clientOld != nil {
		options = clientOld.Options()
	}
	// The stack trace is not useful for 500 errors since it just shows this middleware
	options.AttachStacktrace = false
	// See: https://docs.sentry.io/platforms/go/usage/sdk-fingerprinting/
	options.BeforeSend = SentryFingerprint
	client, err := sentry.NewClient(options)
	if err != nil {
		return hub
	}
	return sentry.NewHub(client, scope)
}

type LogSentrySendFailures struct {
	RT     http.RoundTripper
	Logger *zap.SugaredLogger
}

func (lsf LogSentrySendFailures) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return lsf.RT.RoundTrip(req)
	}

	// copy the body
	var buf bytes.Buffer
	tee := io.TeeReader(req.Body, &buf)
	defer req.Body.Close()
	req.Body = io.NopCloser(&buf)

	resp, err := lsf.RT.RoundTrip(req)
	var statusCode int
	if resp != nil {
		statusCode = resp.StatusCode
	}
	if statusCode >= 400 || resp == nil {
		body, err := io.ReadAll(tee)
		if err != nil {
			lsf.Logger.Errorw("Sentry event send failure: error recovering request body", "status", statusCode, "error", err)
		} else {
			event := sentry.Event{}
			if err := json.Unmarshal(body, &event); err != nil {
				lsf.Logger.Errorw("Sentry event send failure: error recovering request json", "status", statusCode, "error", err)
			}
			var rsp string
			if resp != nil {
				var bufRsp bytes.Buffer
				teeRsp := io.TeeReader(resp.Body, &bufRsp)
				defer resp.Body.Close()
				resp.Body = io.NopCloser(&bufRsp)
				rspBody, err := io.ReadAll(teeRsp)
				if err == nil {
					lsf.Logger.Errorw("Sentry event send failure: error reading response body", "status", statusCode, "error", err)
				} else {
					rsp = string(rspBody)
				}
			}

			lsf.Logger.Errorw("Sentry event", "status", statusCode, "Exception", event.Exception, "response", rsp)
		}
	}
	return resp, err
}
