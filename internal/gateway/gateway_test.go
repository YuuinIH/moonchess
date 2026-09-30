package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/gateway"
)

type localityPlane struct{ control.Plane }

func (*localityPlane) Get(context.Context, string) (control.Record, error) {
	return control.Record{GameID: "g", OwnerID: "owner", CommittedSeq: 8, HeadRef: "head-8"}, nil
}
func (*localityPlane) GetProgress(_ context.Context, _ string, worker string) (control.Progress, error) {
	if worker == "warm" {
		return control.Progress{Seq: 8, HeadRef: "head-8"}, nil
	}
	return control.Progress{Seq: 6, HeadRef: "head-6"}, nil
}
func TestMigrationChoosesCanonicalWarmWorker(t *testing.T) {
	chosen := ""
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Target       string `json:"target"`
			ExpectedHead string `json:"expectedHead"`
			ExpectedSeq  int64  `json:"expectedSeq"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ExpectedHead == "head-8" && body.ExpectedSeq == 8 {
			chosen = body.Target
		}
		w.WriteHeader(200)
	}))
	defer owner.Close()
	health := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				_, _ = w.Write([]byte(`{"seq":8,"headRef":"head-8"}`))
				return
			}
			w.WriteHeader(200)
		}))
	}
	warm := health()
	defer warm.Close()
	cold := health()
	defer cold.Close()
	g := &gateway.Gateway{Plane: &localityPlane{}, WorkerEndpoints: map[string]string{"owner": owner.URL, "warm": warm.URL, "cold": cold.URL}, Client: owner.Client()}
	req := httptest.NewRequest(http.MethodPost, "/api/games/g/migrate", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || chosen != "warm" {
		t.Fatalf("status=%d target=%q body=%s", rec.Code, chosen, rec.Body.String())
	}
}
