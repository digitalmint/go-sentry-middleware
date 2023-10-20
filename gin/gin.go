package sentrygin

import (
	"bytes"

	mdlwrsentry "github.com/digitalmint/go-sentry-middleware"
	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
)

type Sentry500Options struct {
	ExtractContext    func(*gin.Context, *sentry.Scope)
	NoLogResponseBody bool
}

func MiddlewareSentry500(ctx *gin.Context) {
	MiddlewareSentry500Opts(Sentry500Options{})(ctx)
}

func MiddlewareSentry500Opts(opts Sentry500Options) func(*gin.Context) {
	return func(ctx *gin.Context) {
		blw := &bodyLogWriter{body: bytes.NewBufferString(""), ResponseWriter: ctx.Writer}
		ctx.Writer = blw
		ctx.Next()
		if statusCode := ctx.Writer.Status(); statusCode == 500 {
			hubOrig := sentry.GetHubFromContext(ctx.Request.Context())
			if hubOrig == nil {
				hubOrig = sentry.CurrentHub().Clone()
			}
			hub := mdlwrsentry.HubCustomFingerprint(hubOrig, mdlwrsentry.DefaultFingerprintErrorHandler)
			hub.Scope().SetRequest(ctx.Request)
			urlStr := ""
			if url := ctx.Request.URL; url != nil {
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
				err500.Body = blw.body.String()
			}
			hub.CaptureException(err500)
		}
	}
}

type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}
