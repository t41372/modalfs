package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSONRejectsMissingPatchBody(t *testing.T) {
	var input map[string]any
	if err := readJSON(httptest.NewRequest(http.MethodPatch, "/api/settings", nil), &input); err == nil {
		t.Fatal("an empty PATCH must not report success without changing anything")
	}
	if err := readJSON(httptest.NewRequest(http.MethodPost, "/api/shutdown", nil), &input); err != nil {
		t.Fatalf("bodyless actions must still work: %v", err)
	}
	if err := readJSON(httptest.NewRequest(http.MethodPatch, "/api/settings", strings.NewReader(`{"appearance":"dark"}`)), &input); err != nil || input["appearance"] != "dark" {
		t.Fatalf("valid PATCH was not decoded: %v, %v", input, err)
	}
}
