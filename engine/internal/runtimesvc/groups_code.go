package runtimesvc

import (
	"context"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/codefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The code-identity read. `status` has always answered "is the process running
// the code on disk now?" for one worktree; the app already reads the same group
// rows but used to have no way to ask that question. This method is that
// question and nothing else: it compares only running services, on demand, and
// returns the same nullable fields `status` publishes. It is not a second
// runtime fact and the scanner never calls it.
func init() {
	RegisterHandler("groups.code", handleGroupsCode)
}

func handleGroupsCode(ctx context.Context, req *Request) (any, error) {
	var p rpc.GroupsCodeParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "name is required",
			`send {"name": "<group>"}`)
	}
	groups, err := readGroups(req.Runtime)
	if err != nil {
		return nil, err
	}
	var group *state.Group
	for i := range groups {
		if groups[i].Name == name {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		return nil, rpc.NewError(rpc.CodeNotFound, "no group named "+name,
			"run `oberth status` to inspect this worktree")
	}
	codefacts.Annotate(ctx, group, nil)
	return rpc.GroupsCodeResult{Services: serviceCodes(*group)}, nil
}

// serviceCodes keeps only running services, because a stopped one has no
// process to compare against. A running service still appears when every
// field is null: the row says "this run exists and its code identity is
// unknown", which is different from omitting the service.
func serviceCodes(group state.Group) []rpc.ServiceCode {
	out := make([]rpc.ServiceCode, 0, len(group.Services))
	for _, svc := range group.Services {
		if !svc.Running || svc.RunID == nil || *svc.RunID == "" {
			continue
		}
		out = append(out, rpc.ServiceCode{
			Name:         svc.Name,
			RunID:        *svc.RunID,
			CodeHead:     svc.CodeHead,
			Changed:      svc.CodeChangedSinceStart,
			ChangedFiles: svc.CodeChangedFiles,
			Hint:         svc.CodeHint,
		})
	}
	return out
}
