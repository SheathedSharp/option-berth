package attention

// OptionPolicy is the fixed, human-facing vocabulary shared by the artifact,
// Jev adapter, and agent skill. It intentionally describes a safe action
// class rather than carrying shell text or command arguments.
type OptionPolicy struct {
	ID                     string
	Description            string
	ReadOnly               bool
	RequiresExplicitChoice bool
}

var fixedOptionCatalog = []OptionPolicy{
	{ID: "inspect_logs", Description: "Read the existing service log through the documented logs command.", ReadOnly: true},
	{ID: "inspect_port", Description: "Re-read the worktree's observed listener status.", ReadOnly: true},
	{ID: "inspect_dependency", Description: "Re-read declared machine dependency status; do not change that machine service.", ReadOnly: true},
	{ID: "inspect_manifest", Description: "Read the current worktree manifest and its pending draft metadata.", ReadOnly: true},
	{ID: "restart_service", Description: "Restart the named worktree service through the existing restart command.", RequiresExplicitChoice: true},
	{ID: "start_service", Description: "Start the named worktree service through the existing up command.", RequiresExplicitChoice: true},
	{ID: "stop_service", Description: "Stop this project's option-berth services through the existing down command.", RequiresExplicitChoice: true},
	{ID: "adopt_manifest", Description: "Adopt a selected manifest draft through init adopt after the person approves it.", RequiresExplicitChoice: true},
	{ID: "continue_without_action", Description: "Continue the development task and leave runtime state unchanged.", ReadOnly: true},
	{ID: "ask_user_for_context", Description: "Ask the person for missing context before selecting an action.", ReadOnly: true, RequiresExplicitChoice: true},
}

// OptionCatalog returns a copy so callers cannot mutate the shared fixed
// vocabulary. New IDs must be added here and documented in the distributable
// agent skill before they can enter an artifact or a Jev question.
func OptionCatalog() []OptionPolicy {
	result := make([]OptionPolicy, len(fixedOptionCatalog))
	copy(result, fixedOptionCatalog)
	return result
}

func OptionPolicyFor(id string) (OptionPolicy, bool) {
	for _, policy := range fixedOptionCatalog {
		if policy.ID == id {
			return policy, true
		}
	}
	return OptionPolicy{}, false
}

func KnownOptionID(id string) bool {
	_, ok := OptionPolicyFor(id)
	return ok
}
