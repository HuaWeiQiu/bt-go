package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddMagnetBeforeMetadataReturnsTask(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	handler := newMux(srv)
	body := `{"magnet":"magnet:?xt=urn:btih:0000000000000000000000000000000000000001&dn=bt-go-test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"metadata"`) {
		t.Fatalf("expected metadata status, got body=%s", rec.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/tasks/0000000000000000000000000000000000000001", nil)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected delete status 200, got %d body=%s", delRec.Code, delRec.Body.String())
	}
}
