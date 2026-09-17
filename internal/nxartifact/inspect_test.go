package nxartifact

import (
	"bytes"
	"net/http"
	"testing"
)

func TestInspectNxRunBuild(t *testing.T) {
	payload, err := Pack("> nx run web:build:production\nbuilt ok\n", 0, map[string][]byte{
		"outputs/apps/web/dist/main.js": []byte("console.log(1)"),
	})
	if err != nil {
		t.Fatal(err)
	}
	info := Inspect(bytes.NewReader(payload))
	if info.Project != "web" || info.Target != "build" || info.Config != "production" {
		t.Fatalf("%+v", info)
	}
	if info.Kind != "build" {
		t.Fatalf("kind %s", info.Kind)
	}
}

func TestInspectScopedLintAndHeaders(t *testing.T) {
	payload, err := Pack("\x1b[32m> nx run @acme/api:lint\x1b[0m\n", 0, map[string][]byte{
		"outputs/libs/api/.eslintcache": []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	info := Inspect(bytes.NewReader(payload))
	if info.Project != "@acme/api" || info.Target != "lint" || info.Kind != "lint" {
		t.Fatalf("%+v", info)
	}

	h := http.Header{}
	h.Set("X-Nx-Project", "shop")
	h.Set("X-Nx-Target", "test")
	merged := Merge(FromHeaders(h), info)
	if merged.Project != "shop" || merged.Target != "test" || merged.Kind != "test" {
		t.Fatalf("headers should win: %+v", merged)
	}
}

func TestInspectRunningTargetLineAndTestPaths(t *testing.T) {
	payload, err := Pack(" NX   Successfully ran target test for project api\n", 0, map[string][]byte{
		"outputs/coverage/api/lcov.info": []byte("TN:"),
	})
	if err != nil {
		t.Fatal(err)
	}
	info := Inspect(bytes.NewReader(payload))
	if info.Project != "api" || info.Target != "test" || info.Kind != "test" {
		t.Fatalf("%+v", info)
	}
}

func TestInspectPlainBytes(t *testing.T) {
	info := Inspect(bytes.NewReader([]byte("not-a-tar")))
	if !info.Empty() {
		t.Fatalf("expected empty, got %+v", info)
	}
}

func TestKindFromTarget(t *testing.T) {
	if KindFromTarget("e2e") != "e2e" || KindFromTarget("eslint") != "lint" || KindFromTarget("tsc") != "typecheck" {
		t.Fatal(KindFromTarget("e2e"), KindFromTarget("eslint"), KindFromTarget("tsc"))
	}
	if KindFromTarget("typescript") != "typescript" {
		t.Fatalf("tsc substring should not steal: %s", KindFromTarget("typescript"))
	}
}
