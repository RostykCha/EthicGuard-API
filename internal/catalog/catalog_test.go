package catalog

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadAllEntries(t *testing.T) {
	cat, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cat.Keys()) == 0 {
		t.Fatal("expected at least one key")
	}
}

func TestResolveHappyPath(t *testing.T) {
	cat := mustLoad(t)
	got, err := cat.Resolve(
		"ambiguity.vague_quantifier",
		map[string]string{"field": "description", "term": "several"},
		RoleDefault,
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(got, "description") || !strings.Contains(got, "several") {
		t.Fatalf("rendered text missing params: %q", got)
	}
}

func TestResolveUnknownKey(t *testing.T) {
	cat := mustLoad(t)
	_, err := cat.Resolve("nope.not_real", map[string]string{"field": "summary"}, RoleDefault)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
}

func TestResolveRejectsMissingParam(t *testing.T) {
	cat := mustLoad(t)
	_, err := cat.Resolve(
		"ambiguity.vague_quantifier",
		map[string]string{"field": "description"}, // missing term
		RoleDefault,
	)
	if !errors.Is(err, ErrParamMismatch) {
		t.Fatalf("want ErrParamMismatch, got %v", err)
	}
}

func TestResolveRejectsExtraParam(t *testing.T) {
	cat := mustLoad(t)
	_, err := cat.Resolve(
		"untestable.subjective",
		map[string]string{"field": "description", "bonus": "x"},
		RoleDefault,
	)
	if !errors.Is(err, ErrParamMismatch) {
		t.Fatalf("want ErrParamMismatch, got %v", err)
	}
}

func TestResolveRejectsFreeTextParam(t *testing.T) {
	// This is the zero-retention guardrail: if the LLM tries to stuff a
	// sentence into params, the catalog must refuse to render.
	cat := mustLoad(t)
	_, err := cat.Resolve(
		"untestable.subjective",
		map[string]string{"field": "user should feel happy and the experience should be delightful"},
		RoleDefault,
	)
	if !errors.Is(err, ErrParamValueRejected) {
		t.Fatalf("want ErrParamValueRejected, got %v", err)
	}
}

func TestEveryEntryRendersWithStubParams(t *testing.T) {
	cat := mustLoad(t)
	for _, key := range cat.Keys() {
		entry := cat.entries[key]
		params := make(map[string]string, len(entry.params))
		for _, p := range entry.params {
			params[p] = "stub"
		}
		if _, err := cat.Resolve(key, params, RoleDefault); err != nil {
			t.Errorf("%s: Resolve with stub params failed: %v", key, err)
		}
	}
}

func mustLoad(t *testing.T) *Catalog {
	t.Helper()
	cat, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cat
}
