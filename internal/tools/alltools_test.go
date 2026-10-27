package tools

// alltools_test.go: one canonical enumeration of every tool the
// composition root can register — the leaf registry plus the
// runtime-wired tools. The schema and doc-parity tests enumerate
// through this helper so a new tool cannot skip a check by only ever
// being added to cmd/yaah wiring.

func allBuiltinTools() []Tool {
	all := []Tool{
		&ApplyOutTool{},
		&BackgroundProcessTool{},
		&DelegateTool{},
		&ListSubAgentsTool{},
		&MemoryAddTool{},
		&MemoryDeleteTool{},
		&MemoryPromoteTool{},
		&MemorySearchTool{},
		&MemorySessionSearchTool{},
		&MemoryUpdateTool{},
		&PlanTool{},
		&ProposalTool{},
		&RoleTool{},
		&SkillTool{},
		&SubAgentJobsTool{},
		&SupervisorTool{},
	}
	all = append(all, &TodoWriteTool{})
	for name := range leafTools {
		all = append(all, NewLeafTool(name))
	}
	return all
}
