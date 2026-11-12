package desktop

func ParentIndex(ids []string, selected string) int {
	for i, id := range ids {
		if id == selected && selected != "" {
			return i + 1
		}
	}
	return 0
}
func FilterContext(ctx map[string]any, selected string) map[string]any {
	if selected == "" || ctx == nil {
		return ctx
	}
	out := make(map[string]any, len(ctx))
	for k, v := range ctx {
		out[k] = v
	}
	if parents, ok := ctx["parents"].(map[string]any); ok {
		filtered := map[string]any{}
		if p, ok := parents[selected]; ok {
			filtered[selected] = p
		}
		out["parents"] = filtered
	}
	if criteria := displayMap(ctx["criteria"]); criteria != nil {
		filtered := map[string]any{}
		for id, value := range criteria {
			if c, ok := value.(map[string]any); ok && c["parent_id"] == selected {
				filtered[id] = value
			}
		}
		out["criteria"] = filtered
	}
	return out
}
