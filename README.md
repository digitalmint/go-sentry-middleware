# Go Sentry Middleware tools

## 500 middlewares

Send a 500 response to Sentry.

* gin Middleware
* goa Middleware

## Log sentry events that are not sent

Sentry does not provide a way to log information about what is not sent.
This repo implements an http.RoundTripper that can recover and log the sentry.Event that failed to send.
