package atomic_cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The dev server `drift atomic run` generates for a Go Function compiles against
// the SDK in this repository (cli/ and sdk/ are one repository), for every
// handler shape it can call. A Go build resolves the SDK at @latest, so a
// template naming something the SDK does not have fails every `drift atomic
// run` of a Go Function in a file its author never wrote.
func TestEveryGoDevServerCompilesAgainstTheSDK(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("this test compiles the generated dev servers and needs a Go toolchain on PATH: %v", err)
	}
	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", "sdk"))
	if err != nil {
		t.Fatal(err)
	}
	const handlers = `package main

import (
	"context"

	drift "github.com/ondrift/cloud/sdk/go"
)

type LoginBody struct {
	User string ` + "`json:\"user\"`" + `
}

func PostLogin(body LoginBody, req drift.Request) (int, string, any, map[string]string) {
	return 200, "OK", body.User, nil
}

func PostLoginCtx(ctx context.Context, body LoginBody, req drift.Request) (int, string, any, map[string]string) {
	return 200, "OK", ctx.Err() == nil, nil
}

func GetPing(req drift.Request) (int, string, any, map[string]string) {
	return 200, "OK", req.Path, nil
}

func GetPingCtx(ctx context.Context, req drift.Request) (int, string, any, map[string]string) {
	return 200, "OK", ctx.Err() == nil, nil
}
`
	root := t.TempDir()
	gomod := "module devservers\n\ngo 1.26\n\nrequire github.com/ondrift/cloud/sdk v0.0.0\n\nreplace github.com/ondrift/cloud/sdk => " + sdk + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o600); err != nil {
		t.Fatal(err)
	}
	servers := map[string][2]string{
		"post":     {"PostLogin", "post"},
		"post-ctx": {"PostLoginCtx", "post"},
		"put":      {"PostLogin", "put"},
		"get":      {"GetPing", "get"},
		"get-ctx":  {"GetPingCtx", "get"},
	}
	for name, s := range servers {
		dir := filepath.Join(root, "cmd", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "handlers.go"), []byte(handlers), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := renderGoServer(dir, s[0], s[1]); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		src, err := os.ReadFile(filepath.Join(dir, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "{{") {
			t.Errorf("%s: an unreplaced placeholder survived:\n%s", name, src)
		}
	}
	// Several main packages: go build compiles each and keeps no binary.
	cmd := exec.Command(goBin, "build", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the generated dev servers do not compile against the SDK: %v\n%s", err, out)
	}
}
