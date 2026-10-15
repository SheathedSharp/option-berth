package agentstream

func codexStreamItem(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	raw, ok := params["item"].(map[string]any)
	if ok {
		return raw
	}
	return params
}

func (c *codexLive) emitItemUpdated(b *bridge, turn, itemID, kind, delta string, budget *int) {
	if turn == "" || itemID == "" || delta == "" {
		return
	}
	clipped, _ := clipWithBudget(delta, maxTurnMessageBytes, budget)
	if clipped == "" && budget != nil {
		return
	}
	b.emit(map[string]any{
		"v": 1, "type": "item.updated", "turn": turn,
		"item": itemID, "kind": kind, "text_delta": clipped,
	})
}
