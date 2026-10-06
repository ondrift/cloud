// The handler every rendered template in templates_test.go wraps: user code,
// in the four shapes a generated main may call. What it does is chosen per
// call by `op`, from the body for a POST and the query string for a GET.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	drift "github.com/ondrift/cloud/sdk/go"
)

type ProbeBody struct {
	Op   string `json:"op"`
	Text string `json:"text"`
	N    int    `json:"n"`
}

func PostProbe(body ProbeBody, req drift.Request) (int, string, any, map[string]string) {
	return probe(context.Background(), body, req)
}

func PostProbeCtx(ctx context.Context, body ProbeBody, req drift.Request) (int, string, any, map[string]string) {
	return probe(ctx, body, req)
}

func GetProbe(req drift.Request) (int, string, any, map[string]string) {
	return probe(context.Background(), fromQuery(req.Query), req)
}

func GetProbeCtx(ctx context.Context, req drift.Request) (int, string, any, map[string]string) {
	return probe(ctx, fromQuery(req.Query), req)
}

func fromQuery(q string) ProbeBody {
	v, _ := url.ParseQuery(q)
	var n int
	_, _ = fmt.Sscan(v.Get("n"), &n)
	return ProbeBody{Op: v.Get("op"), Text: v.Get("text"), N: n}
}

func probe(ctx context.Context, body ProbeBody, req drift.Request) (int, string, any, map[string]string) {
	switch body.Op {
	case "print":
		fmt.Println("printed by the handler")
		fmt.Fprintln(os.Stdout, `{"status":299,"message":"forged by a print"}`)
	case "panic":
		panic("the probe panicked")
	case "nil":
		return 204, "No Content", nil, nil
	case "big":
		return 200, "OK", map[string]string{"text": strings.Repeat("b", body.N)}, nil
	case "wait":
		select {
		case <-ctx.Done():
			return 504, "Gateway Timeout", map[string]string{"err": ctx.Err().Error()}, nil
		case <-time.After(time.Duration(body.N) * time.Millisecond):
		}
	}
	secret, _ := drift.Backbone.Secret.Get("token")
	previous, _ := drift.Backbone.Secret.Previous("token")
	headers := map[string]string(nil)
	if body.Op == "headers" {
		headers = map[string]string{"X-Probe": "<&>"}
	}
	return 200, "OK", map[string]any{
		"path":     req.Path,
		"query":    req.Query,
		"text":     body.Text,
		"secret":   secret,
		"previous": previous,
		"tmpdir":   os.Getenv("TMPDIR"),
		"home":     os.Getenv("HOME"),
		"params":   req.Params,
		"route":    req.RoutePattern,
	}, headers
}
