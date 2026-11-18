package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"path/filepath"
)

func claudeRuntimeContext(doc statusDocument, root string, scope model.TaskContext) (string, error) {
	if filepath.Clean(doc.Scope.Root) != filepath.Clean(root) {
		return "", fmt.Errorf("runtime worktree mismatch")
	}
	type target struct {
		Name    string `json:"name,omitempty"`
		RunID   string `json:"run_id,omitempty"`
		Running *bool  `json:"running,omitempty"`
		State   string `json:"state,omitempty"`
		Reason  string `json:"reason,omitempty"`
		Health  string `json:"health,omitempty"`
	}
	result := struct {
		Observation *rpc.ObservationStatus `json:"observation,omitempty"`
		Services    []target               `json:"services"`
		Runs        []target               `json:"runs"`
	}{Observation: doc.Observation, Services: []target{}, Runs: []target{}}
	for _, name := range scope.Services {
		found := false
		for _, s := range doc.Worktree.Services {
			if s.Name == name {
				row := target{Name: name}
				if s.Running || !runtimeObservationUnknown(doc) {
					value := s.Running
					row.Running = &value
				} else {
					row.State = "unknown"
				}
				if s.HealthStatus != nil {
					row.Health = s.HealthStatus.Status
				}
				if s.RunID != nil {
					row.RunID = *s.RunID
				}
				if s.LastExit != nil {
					row.Reason = s.LastExit.Reason
				}
				result.Services = append(result.Services, row)
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("service not declared in selected worktree")
		}
	}
	for _, id := range scope.RunIDs {
		found := false
		for _, run := range runtimeRunFacts(doc) {
			if run.ID == id {
				result.Runs = append(result.Runs, target{Name: run.Name, RunID: id, Running: runtimeRunRunning(run), State: run.Status, Reason: run.Reason})
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("run not recorded in selected worktree")
		}
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func runtimeRunRunning(run rpc.RunRecord) *bool {
	switch run.Status {
	case "running", "starting":
		value := true
		return &value
	case "exited":
		value := false
		return &value
	}
	return nil
}
func runtimeObservationUnknown(doc statusDocument) bool {
	return doc.Observation != nil && (doc.Observation.State != "observable" || doc.Observation.Narrow())
}
func runtimeRunFacts(doc statusDocument) []rpc.RunRecord {
	exits := doc.attentionExits
	if exits == nil {
		exits = doc.Exits
	}
	return append(append([]rpc.RunRecord(nil), doc.Runs...), exits...)
}
