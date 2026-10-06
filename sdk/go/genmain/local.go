package genmain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	drift "github.com/ondrift/cloud/sdk/go"
)

// serveLocal serves the handler over HTTP for development outside a slice, on
// $PORT (8080 by default), the way drift.Run does. net/http runs each request
// on its own goroutine, so calls are concurrent here as on a concurrent Pulse.
func serveLocal(handler handlerFunc) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Fprintf(os.Stderr, "drift-sdk: local server starting on :%s\n", port)
	if err := http.ListenAndServe(":"+port, localHandler(handler)); err != nil { // #nosec G114 -- a local development server, started only outside a slice.
		fmt.Fprintf(os.Stderr, "drift-sdk: server error: %v\n", err)
		os.Exit(1)
	}
}

// localHandler answers one HTTP request with the handler's response, in the
// shape drift.Run's local server writes. The handler's context is the
// request's, bounded by the call's runtime budget as on a slice.
func localHandler(handler handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		headers := make(map[string]string)
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		req := drift.Request{
			Path:    r.URL.Path,
			Headers: headers,
			Query:   r.URL.RawQuery,
			Body:    body,
		}

		ctx, cancel := context.WithTimeout(r.Context(), runtimeBudget(headers))
		defer cancel()
		resp := handler(ctx, req)

		w.Header().Set("Content-Type", "application/json")
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  resp.Status,
			"message": resp.Message,
			"payload": resp.Payload,
		})
	})
}
