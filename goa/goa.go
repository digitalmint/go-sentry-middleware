package mdlwrsentrygoa

import (
	"context"
	"net/http"

	mdlwrsentry "github.com/digitalmint/go-sentry-middleware"
	"github.com/getsentry/sentry-go"
)

type Sentry500Options struct {
	ExtractContext    func(context.Context, *sentry.Scope)
	NoLogResponseBody bool
	FingerprintOpts   mdlwrsentry.FingerprintOpts
}

var DefaultSentry500Opts = Sentry500Options{
	FingerprintOpts: mdlwrsentry.DefaultFingerprinter,
}

// MiddlewareSentry500 is a Goa middleware that captures the response status code and sends to Sentry if code=500.
func MiddlewareSentry500(opts Sentry500Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Create a custom response writer to capture the status code
			captureWriter := &statusCaptureResponseWriter{ResponseWriter: w}

			// Call the next middleware/handler in the chain
			next.ServeHTTP(captureWriter, r)

			// Retrieve the captured response status code
			respStatus := captureWriter.statusCode
			if respStatus == 500 {
				ctx := r.Context()
				hubOrig := sentry.GetHubFromContext(ctx)
				if hubOrig == nil {
					hubOrig = sentry.CurrentHub().Clone()
				}
				hub := mdlwrsentry.HubCustomFingerprint(hubOrig, opts.FingerprintOpts)
				hub.Scope().SetRequest(r)
				urlStr := ""
				if url := r.URL; url != nil {
					urlStr = url.String()
				}

				if opts.ExtractContext != nil {
					opts.ExtractContext(ctx, hub.Scope())
				}

				err500 := mdlwrsentry.SentryError500{
					Url:  urlStr,
					Body: "",
				}
				if !opts.NoLogResponseBody {
					err500.Body = captureWriter.body
				}
				hub.CaptureException(err500)
			}

		})
	}
}

// statusCaptureResponseWriter is a custom response writer to capture the status code.
type statusCaptureResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       string
}

// WriteHeader captures the status code before it's written.
func (sw *statusCaptureResponseWriter) WriteHeader(code int) {
	sw.statusCode = code
	sw.ResponseWriter.WriteHeader(code)
}

// Write captures the body before it's written.
func (sw *statusCaptureResponseWriter) Write(b []byte) (int, error) {
	sw.body = string(b)
	return sw.ResponseWriter.Write(b)
}
