package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

func TestTaskSelectionPrecedesProviderAndCache(t *testing.T) {
	status, _ := runnerFixture(t)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			State struct {
				Events     []attn.Event `json:"events"`
				Authorized []string     `json:"authorized_options"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.State.Events) != 1 || body.State.Events[0].RunID != "worker-1" {
			t.Errorf("provider received unrelated event: %+v", body.State.Events)
		}
		probabilities := map[string]float64{}
		for _, id := range body.State.Authorized {
			probabilities[id] = 0
		}
		probabilities["inspect_logs"] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fake", "answers": map[string]any{"task_relevant": map[string]any{"type": "noul", "noul": .9}, "needs_human": map[string]any{"type": "noul", "noul": .9}, "next_option": map[string]any{"type": "choice", "choice": "inspect_logs", "probabilities": probabilities, "confidence": .9}}})
	}))
	defer server.Close()
	client := model.Client{Endpoint: server.URL, Model: "fake", APIKey: "synthetic", HTTPClient: server.Client()}
	read := func(context.Context, string, string) (Status, error) { return status, nil }
	task := model.TaskContext{Task: "inspect selected worker", RunIDs: []string{"foreign-run"}, AuthorizedOptions: []string{"inspect_logs"}}
	out, err := Auto(context.Background(), "unused", ".", client, task, read)
	if err != nil || out.Status != "clear" || calls != 0 {
		t.Fatalf("unrelated scope contacted model: %+v %v calls=%d", out, err, calls)
	}
	task.RunIDs = []string{"worker-1"}
	out, err = Auto(context.Background(), "unused", ".", client, task, read)
	if err != nil || out.Request == nil || len(out.Request.Events) != 1 || calls != 1 {
		t.Fatalf("selected scope: %+v %v calls=%d", out, err, calls)
	}
	out, err = Auto(context.Background(), "unused", ".", client, task, read)
	if err != nil || !out.Cached || calls != 1 {
		t.Fatalf("repeat bought another assessment: %+v %v calls=%d", out, err, calls)
	}
}
