# Refactoring Examples: Current vs Component System

> **Status**: Aspirational design document. Not yet implemented.

These examples compare the current inline styling approach against a proposed
component-based refactor.

## Example 1: User Message Rendering

### Current Approach (render.go)
```go
case "user":
    rendered := userStyle.Render(chatWrap("", msg.Content, m.width))
    b.WriteString(userBgStyle.Width(m.width).Render(rendered))
    b.WriteString("\n")
```

### Component System Approach
```go
case "user":
    userMsg := factory.CreateUserMessage(msg.Content)
    rendered := userMsg.Render()
    b.WriteString(userBgStyle.Width(m.width).Render(rendered))
    b.WriteString("\n")
```

### Benefits
- **Centralized styling**: Padding, margins, and width constraints defined in one place
- **Consistent behavior**: All user messages use the same component
- **Easy to modify**: Change padding once affects all user messages

## Example 2: Assistant Message with Reasoning

### Current Approach
```go
case "assistant":
    if msg.Reasoning != "" {
        b.WriteString("\n")
        zoneID := fmt.Sprintf("reasoning-%d", msgIdx)
        m.reasoningZones = append(m.reasoningZones, zoneID)
        if !m.reasoningExpanded[zoneID] {
            b.WriteString(zone.Mark(zoneID, toggleStyle.Render("  ▶ Reasoning...")))
            b.WriteString("\n")
        } else {
            b.WriteString(zone.Mark(zoneID, toggleStyle.Render("  ▼ Reasoning...")))
            b.WriteString("\n\n")
            b.WriteString(reasoningBgStyle.Width(m.width).Render(
                thinkingStyle.Render(chatWrap("", msg.Reasoning, m.width))))
            b.WriteString("\n")
        }
    }
    b.WriteString("\n")
    b.WriteString(assistantStyle.Render(msg.Content))
    b.WriteString("\n\n")
```

### Component System Approach
```go
case "assistant":
    if msg.Reasoning != "" {
        b.WriteString("\n")
        zoneID := fmt.Sprintf("reasoning-%d", msgIdx)
        m.reasoningZones = append(m.reasoningZones, zoneID)
        
        // Use expandable component for reasoning
        reasoningComp := NewExpandableComponent("Reasoning...", zoneID, m.reasoningExpanded[zoneID]).
            WithContent(NewBaseComponent(msg.Reasoning, func(content string, width int) string {
                return chatWrap("", content, width)
            }).WithWidth(m.width).WithStyle(thinkingStyle)).
            WithStyle(toggleStyle)
        
        b.WriteString(reasoningComp.Render())
        b.WriteString("\n")
    }
    b.WriteString("\n")
    
    // Use component for assistant message
    assistantMsg := factory.CreateAssistantMessage(msg.Content)
    b.WriteString(assistantMsg.Render())
    b.WriteString("\n\n")
```

### Benefits
- **Reusable expandable pattern**: Same component for reasoning, tool output, etc.
- **Consistent toggle behavior**: All expandable sections work the same way
- **Easier to test**: Component logic isolated from rendering

## Example 3: Tool Message with Expansion

### Current Approach
```go
case "tool":
    // Build header...
    header := msg.ToolName
    // ... header building logic ...
    
    icon := "✓"
    if m.toolCall == msg.ToolName {
        icon = "⏳"
    }
    
    zoneID := fmt.Sprintf("tool-%d", msgIdx)
    m.toolZones = append(m.toolZones, zoneID)
    
    expanded, has := m.toolExpanded[zoneID]
    if !has {
        expanded = m.toolCall == msg.ToolName
    }
    
    if expanded {
        b.WriteString(zone.Mark(zoneID, toolStyle.Render(fmt.Sprintf("  ▼ %s %s", icon, header))))
        b.WriteString("\n")
        // Complex tool output rendering...
        boxWidth := m.width - 4
        // ... 20+ lines of rendering logic ...
        b.WriteString(toolBoxStyle.Width(boxWidth).Render(visible))
    } else {
        b.WriteString(zone.Mark(zoneID, toolStyle.Render(fmt.Sprintf("  ▶ %s %s", icon, header))))
    }
    b.WriteString("\n")
```

### Component System Approach
```go
case "tool":
    // Build header...
    header := msg.ToolName
    // ... header building logic ...
    
    icon := "✓"
    if m.toolCall == msg.ToolName {
        icon = "⏳"
    }
    
    zoneID := fmt.Sprintf("tool-%d", msgIdx)
    m.toolZones = append(m.toolZones, zoneID)
    
    expanded, has := m.toolExpanded[zoneID]
    if !has {
        expanded = m.toolCall == msg.ToolName
    }
    
    // Use expandable component with tool-specific rendering
    toolComp := NewExpandableComponent(
        fmt.Sprintf("%s %s", icon, header),
        zoneID,
        expanded,
    ).WithContent(factory.CreateToolMessage(msg.ToolName, msg.Content)).
        WithStyle(toolStyle)
    
    b.WriteString(toolComp.Render())
    b.WriteString("\n")
```

### Benefits
- **Simplified expansion logic**: Component handles expand/collapse state
- **Consistent tool rendering**: All tools use the same component
- **Easier to maintain**: Tool-specific logic contained in component

## Example 4: Status Bar

### Current Approach
```go
var statusText string
ctxBar := ""
if m.contextWindow > 0 {
    ctxBar = " " + contextBar(m.contextPct)
}
statusText = fmt.Sprintf(" %s │ messages: %d │%s",
    shortenCWD(m.cwd, m.width/3), len(m.messages), ctxBar)
status := statusStyle.Width(m.width).Render(statusText)
```

### Component System Approach
```go
statusContent := fmt.Sprintf(" %s │ messages: %d │%s",
    shortenCWD(m.cwd, m.width/3), len(m.messages), ctxBar)
status := factory.CreateStatus(statusContent).Render()
```

### Benefits
- **Consistent status styling**: All status elements use same padding/margins
- **Easy to modify**: Change status styling in one place
- **Reusable pattern**: Can create other status-like components

## Example 5: Command Palette

### Current Approach
```go
var palette string
if m.showHelp {
    palette = m.renderHelpOverlay()
} else if m.questionMode {
    palette = m.renderQuestionModal()
} else if m.modelMode {
    palette = m.renderModelPalette()
} else if m.commandMode {
    palette = m.renderCommandPalette()
}
```

### Component System Approach
```go
var palette Component
if m.showHelp {
    palette = m.renderHelpOverlayComponent()
} else if m.questionMode {
    palette = m.renderQuestionModalComponent()
} else if m.modelMode {
    palette = m.renderModelPaletteComponent()
} else if m.commandMode {
    palette = m.renderCommandPaletteComponent()
}

if palette != nil {
    elements = append(elements, palette.Render())
}
```

### Benefits
- **Consistent palette styling**: All palettes use same component
- **Type safety**: Components return Component interface
- **Easier to test**: Each palette is independent component

## Migration Strategy

### Phase 1: Foundation (Week 1)
1. Implement `component.go` with base interfaces
2. Create `MessageComponentFactory` for message types
3. Refactor user and assistant message rendering

### Phase 2: Expansion (Week 2)
1. Implement `ExpandableComponent` for reasoning and tool output
2. Refactor tool message rendering
3. Add `ComponentFactory` for consistent styling

### Phase 3: Utilities (Week 3)
1. Implement `CompositeComponent` for complex layouts
2. Add `ConditionalComponent` for dynamic content
3. Create `ZoneComponent` for hover/click support

### Phase 4: Integration (Week 4)
1. Refactor `renderMessages()` to use component system
2. Update `View()` method to use components
3. Add component-level tests

## Testing Strategy

### Unit Tests for Components
```go
func TestBaseComponent_Render(t *testing.T) {
    comp := NewBaseComponent("test content", func(content string, width int) string {
        return content
    }).WithWidth(80).WithPadding(1, 1, 1, 1)
    
    rendered := comp.Render()
    // Assert padding is applied correctly
    assert.Contains(t, rendered, " test content ")
}

func TestExpandableComponent_Render(t *testing.T) {
    comp := NewExpandableComponent("Header", "zone-1", false).
        WithContent(NewBaseComponent("content", nil))
    
    rendered := comp.Render()
    assert.Contains(t, rendered, "▶ Header")
    assert.NotContains(t, rendered, "content")
    
    comp.SetExpanded(true)
    rendered = comp.Render()
    assert.Contains(t, rendered, "▼ Header")
    assert.Contains(t, rendered, "content")
}
```

### Integration Tests
```go
func TestMessageRendering_Consistency(t *testing.T) {
    factory := NewMessageComponentFactory(80)
    
    userMsg := factory.CreateUserMessage("test")
    assistantMsg := factory.CreateAssistantMessage("test")
    
    // Both should have consistent width
    assert.Equal(t, 80, userMsg.GetWidth())
    assert.Equal(t, 80, assistantMsg.GetWidth())
}
```

## Performance Considerations

1. **Component Creation Overhead**: Minimal - components are lightweight structs
2. **Rendering Performance**: Same as current approach - lipgloss rendering is the bottleneck
3. **Memory Usage**: Slight increase due to component structs, but negligible
4. **Startup Time**: No impact - components created on demand

## Backward Compatibility

1. **Gradual Migration**: Can refactor one component at a time
2. **Fallback Support**: Keep old render functions as fallbacks
3. **Theme Compatibility**: Component system works with existing themes
4. **Zone System**: Components integrate with existing zone/hover system