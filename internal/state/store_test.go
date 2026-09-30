package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	statepkg "github.com/yuuinih/moonchess/internal/state"
)

func TestMooncakeStorePutAndGet(t *testing.T) {
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/put":
			var body struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Key != "game/g1/epoch/1/seq/0" {
				t.Fatalf("key = %q", body.Key)
			}
			stored = []byte(body.Value)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success"}`)
		case r.Method == http.MethodGet:
			_, _ = w.Write(stored)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := statepkg.NewMooncakeHTTPStore(server.URL, server.Client())
	ctx := context.Background()
	if err := store.Put(ctx, "game/g1/epoch/1/seq/0", []byte(`{"turn":"white"}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(ctx, "game/g1/epoch/1/seq/0")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != `{"turn":"white"}` {
		t.Fatalf("get = %s", got)
	}
}

func TestMooncakeStoreReportsMissingKey(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	store := statepkg.NewMooncakeHTTPStore(server.URL, server.Client())
	_, err := store.Get(context.Background(), "missing")
	if !errors.Is(err, statepkg.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
