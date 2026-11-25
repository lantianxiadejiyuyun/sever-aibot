package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetInventoryRouteDoesNotRedirectToStaticDirectory(t *testing.T) {
	for _, path := range []string{"/assets", "/assets/", "/assets/ast_missing", "/"} {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Location") != "" {
			t.Fatalf("%s code=%d headers=%v", path, w.Code, w.Header())
		}
	}
}
