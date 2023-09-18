package sentry

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/getsentry/sentry-go"
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
			newPath, err := NormalizeUrlPathForSentry(ex.Url, "")
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

func RedactDSN(body []byte) []byte {
	re, err := regexp.Compile(`[^ '"]+sentry.io/[^ '"]+`)
	if err != nil {
		panic(err)
	}
	return re.ReplaceAll(body, []byte("REDACTED"))
}

func (lsf LogSentrySendFailures) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return lsf.RT.RoundTrip(req)
	}

	// copy the request body
	var buf bytes.Buffer
	tee := io.TeeReader(req.Body, &buf)
	defer req.Body.Close()
	req.Body = io.NopCloser(tee)
	resp, err := lsf.RT.RoundTrip(req)
	req.Body = io.NopCloser(&buf)

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
				lsf.Logger.Errorw(
					"Sentry event send failure: error recovering request json",
					"status", statusCode,
					"error", err,
					"request", string(RedactDSN(body)),
				)
			}
			var rspBodyStr string
			if resp != nil {
				// copy the response body
				var bufRsp bytes.Buffer
				teeRsp := io.TeeReader(resp.Body, &bufRsp)
				defer resp.Body.Close()
				resp.Body = io.NopCloser(&bufRsp)
				rspBody, err := io.ReadAll(teeRsp)
				if err != nil {
					lsf.Logger.Errorw(
						"Sentry event send failure: error reading response body",
						"status", statusCode,
						"error", err,
					)
				} else {
					rspBodyStr = string(RedactDSN(rspBody))
				}
			}

			lsf.Logger.Errorw(
				"Sentry event",
				"status", statusCode,
				"Exception", event.Exception,
				"response", rspBodyStr,
			)
		}
	}
	return resp, err
}

// NormalizeUrlPathForSentry takes a url path string and replaces any path part that contains a number with a standard placeholder value.
// This allows for better error grouping at Sentry for urls that may contain dynamic values (UUID for example) but are basically the same URL in general
func NormalizeUrlPathForSentry(_url string, placeholder string) (string, error) {
	if placeholder == "" {
		placeholder = "-omitted-"
	}
	newPath := _url
	u, err := url.Parse(_url)
	if err != nil {
		return newPath, err
	} else { // Split the URL path into individual parts
		pathParts := strings.Split(u.Path, "/")

		// Regular expression to match numeric parts of the path
		numericRegex := regexp.MustCompile("[0-9]+")

		// Iterate over each part of the path
		for i, part := range pathParts {
			// Check if the part contains a number
			if part != "v1" && part != "v2" && numericRegex.MatchString(part) {
				// Replace the numeric part with "placeholder"
				pathParts[i] = placeholder
			}
		}

		// Join the path parts back into a single string with "/"
		newPath = strings.Join(pathParts, "/")
		newPath = strings.TrimSuffix(newPath, "/")
	}
	return newPath, nil
}
