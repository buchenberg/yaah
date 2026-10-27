package tools

// alltools_test.go: the canonical roll of every tool the composition
// root can register — the leaf registry (which leafTools, a code-level
// map, backs directly) plus the runtime-wired tools. The wiring half is
// hand-maintained: when you register a tool in cmd/yaah, add it here,
// because the schema and doc-parity tests both walk this list and a
// tool missing from it skips both.

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
