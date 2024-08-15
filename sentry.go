package sentry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/getsentry/sentry-go"
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
func SentryFingerprint(event *sentry.Event, hint *sentry.EventHint) error {
	if oe := hint.OriginalException; oe != nil {
		//nolint:errorlint
		if ex, ok := oe.(SentryError500); ok {
			message := ex.Body
			if len(ex.Body) > 15 {
				message = ex.Body[0:15]
			}
			u, err := url.Parse(ex.Url)
			if err != nil {
				return err
			}
			newPath := NormalizeUrlPathForSentry(u, "")
			event.Fingerprint = []string{newPath, message}
		}
	}
	return nil
}

func DefaultFingerprintErrorHandler(err error) {
	slog.Error("error during fingerprinting", "error", err)
}

func HubCustomFingerprint(hub *sentry.Hub, fingerprintErrHandler func(err error)) *sentry.Hub {
	clientOld, scope := hub.Client(), hub.Scope()
	options := sentry.ClientOptions{}
	if clientOld != nil {
		options = clientOld.Options()
	}
	// The stack trace is not useful for 500 errors since it just shows this middleware
	options.AttachStacktrace = false
	// See: https://docs.sentry.io/platforms/go/usage/sdk-fingerprinting/
	options.BeforeSend = func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
		err := SentryFingerprint(event, hint)
		if err != nil {
			fingerprintErrHandler(err)
		}
		return event
	}
	client, err := sentry.NewClient(options)
	if err != nil {
		return hub
	}
	return sentry.NewHub(client, scope)
}

type LogSentrySendFailures struct {
	RT           http.RoundTripper
	ErrorHandler func(context.Context, ErrSentryRoundTrip)
}

func NewLogSentrySendFailures(rt http.RoundTripper) LogSentrySendFailures {
	return LogSentrySendFailures{RT: rt, ErrorHandler: SlogErrHandler}
}

func SlogErrHandler(ctx context.Context, err ErrSentryRoundTrip) {
	attrs := make([]slog.Attr, 0, 4)
	if err.Status != 0 {
		attrs = append(attrs, slog.Int("status", err.Status))
	}
	if err.Exception != nil {
		attrs = append(attrs, slog.String("exception", fmt.Sprintf("%v", err.Exception)))
	}
	if err.Request != nil {
		attrs = append(attrs, slog.String("request", string(err.Request)))
	}
	if err.Response != nil {
		attrs = append(attrs, slog.String("response", string(err.Response)))
	}
	slog.LogAttrs(ctx, slog.LevelError, err.Msg, attrs...)
}

func RedactDSN(body []byte) []byte {
	re, err := regexp.Compile(`[^ '"]+sentry\.io/[^ '"]+`)
	if err != nil {
		panic(err)
	}
	return re.ReplaceAll(body, []byte("REDACTED"))
}

type ErrSentryRoundTrip struct {
	Msg       string
	Err       error
	Status    int
	Request   []byte
	Exception []sentry.Exception
	Response  []byte
}

func (esrt ErrSentryRoundTrip) Error() string {
	var attrs string
	if esrt.Status != 0 {
		attrs = attrs + fmt.Sprintf(" status=%d", esrt.Status)
	}
	if esrt.Exception != nil {
		attrs = attrs + fmt.Sprintf(" exception=%v", esrt.Request)
	}
	if esrt.Request != nil {
		attrs = attrs + " request=" + string(esrt.Request)
	}
	if esrt.Response != nil {
		attrs = attrs + " response=" + string(esrt.Response)
	}
	return esrt.Msg + ": " + esrt.Err.Error() + " " + attrs
}

func (lsf LogSentrySendFailures) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return lsf.RT.RoundTrip(req)
	}
	ctx := req.Context()

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
			lsf.ErrorHandler(ctx, ErrSentryRoundTrip{
				Msg:    "Sentry event send failure: error recovering request body",
				Err:    err,
				Status: statusCode,
			})
		} else {
			event := sentry.Event{}
			if err := json.Unmarshal(body, &event); err != nil {
				lsf.ErrorHandler(ctx, ErrSentryRoundTrip{
					Msg:     "Sentry event send failure: error recovering request json",
					Err:     err,
					Status:  statusCode,
					Request: RedactDSN(body),
				})
			}
			var rspBody []byte
			if resp != nil {
				// copy the response body
				var bufRsp bytes.Buffer
				teeRsp := io.TeeReader(resp.Body, &bufRsp)
				defer resp.Body.Close()
				resp.Body = io.NopCloser(&bufRsp)
				var err error
				rspBody, err = io.ReadAll(teeRsp)
				if err != nil {
					lsf.ErrorHandler(ctx, ErrSentryRoundTrip{
						Msg:    "Sentry event send failure: error reading response body",
						Err:    err,
						Status: statusCode,
					})
				}
			}

			lsf.ErrorHandler(ctx, ErrSentryRoundTrip{
				Msg:       "Sentry event",
				Status:    statusCode,
				Exception: event.Exception,
				Response:  RedactDSN(rspBody),
			})
		}
	}
	return resp, err
}

// NormalizeUrlPathForSentry takes a url path string and replaces any path part that contains a number with a standard placeholder value.
// This allows for better error grouping at Sentry for urls that may contain dynamic values (UUID for example) but are basically the same URL in general
func NormalizeUrlPathForSentry(url *url.URL, placeholder string) string {
	if placeholder == "" {
		placeholder = "-omitted-"
	}
	pathParts := strings.Split(url.Path, "/")

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
	newPath := strings.Join(pathParts, "/")
	newPath = strings.TrimSuffix(newPath, "/")
	return newPath
}

type UnwrapAndFilterErrorTypeConfig struct {
	FilterErrorTypes []string
}

// Golang error types tend to be generic wrappers
// Unwrap known generic error types until we find an unrecognized error type
// That error type is assumed to be useful
// Otherwise just strip the "*errors." or "errors." prefix which adds noise
func SentryBeforeSendUnwrapAndFilterErrorType(conf UnwrapAndFilterErrorTypeConfig) func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	if len(conf.FilterErrorTypes) == 0 {
		conf.FilterErrorTypes = defaultFilterErrorTypes
	}
	return conf.sentryBeforeSendUnwrapAndFilterErrorType
}

func (conf UnwrapAndFilterErrorTypeConfig) sentryBeforeSendUnwrapAndFilterErrorType(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	oe := hint.OriginalException
	if oe == nil {
		return event
	}
	errStr := unwrapToSpecificError(oe, conf.FilterErrorTypes)
	exLastIndex := len(event.Exception) - 1
	if errStr != nil && *errStr != event.Exception[exLastIndex].Type {
		event.Exception[exLastIndex].Type = *errStr
	}
	return event
}

var defaultFilterErrorTypes = []string{"errors.", "fmt.wrapError"}

func unwrapToSpecificError(err error, filterErrorTypes []string) *string {
	var typStr string
	var firstTypStr string
	var underlying error
	for {
		typ := reflect.TypeOf(err)
		if typ == nil {
			break
		}
		typStr = typ.String()

		var found bool
		var after string
		for _, prefix := range filterErrorTypes {
			after, found = strings.CutPrefix(typStr, prefix)
			if found {
				typStr = after // remove the non-useful prefix
				break
			}
			after, found = strings.CutPrefix(typStr, "*"+prefix)
			if found {
				typStr = after // remove the non-useful prefix
				break
			}
		}

		if firstTypStr == "" {
			firstTypStr = typStr
		}
		// Look for a non-filtered error type
		if found {
			if underlying = errors.Unwrap(err); underlying != nil {
				err = underlying
				continue
			}
		}
		break
	}

	if typStr == "" {
		return nil
	}

	if underlying == nil {
		return &firstTypStr
	} else {
		return &typStr
	}
}
