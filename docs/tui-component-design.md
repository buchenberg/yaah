# TUI Component System Design

## Problem Statement
The current TUI implementation has repetitive styling patterns scattered throughout `render.go`. Styles are applied inline with manual width calculations, padding, and margin adjustments. This makes it difficult to maintain consistent styling across components and requires changes in multiple places when adjusting global properties.

## Proposed Solution: React-like Component System

### Core Concept
Create a `Component` abstraction that encapsulates:
- Content rendering
- Styling properties (padding, margin, width, etc.)
- Composition (nesting components)

### Component Interface
```go
type Component interface {
    // Render produces the final string representation
    Render() string
    
    // WithWidth sets the component width
    WithWidth(width int) Component
    
    // WithPadding sets padding (top, right, bottom, left)
    WithPadding(top, right, bottom, left int) Component
    
    // WithMargin sets margin (top, right, bottom, left)
    WithMargin(top, right, bottom, left int) Component
    
    // WithStyle applies a lipgloss style
    WithStyle(style lipgloss.Style) Component
    
    // GetWidth returns the current width
    GetWidth() int
}
```

### Base Component
A `BaseComponent` struct that implements common properties:
```go
type BaseComponent struct {
    content  string
    width    int
    padding  [4]int // top, right, bottom, left
    margin   [4]int // top, right, bottom, left
    style    lipgloss.Style
    renderer func(content string, width int) string
}
```

### Component Types
1. **MessageComponent** - For chat messages (user, assistant, tool, system)
2. **StatusComponent** - For status bar
3. **HeaderComponent** - For title/banner area
4. **PaletteComponent** - For command/model selection overlays
5. **InputComponent** - For text input area

### Benefits
1. **Consistent Styling**: All components share the same padding/margin logic
2. **Easy Global Changes**: Modify padding in one place affects all components
3. **Reusability**: Components can be composed and nested
4. **Maintainability**: Styling logic is centralized
5. **Testability**: Components can be tested independently

### Migration Path
1. Create `component.go` with base interfaces and structs
2. Create specific component files (`message_component.go`, `status_component.go`, etc.)
3. Gradually refactor `render.go` to use components
4. Update `theme.go` to provide component-level styling defaults

### Example Usage
```go
// Current approach (scattered throughout render.go)
rendered := userStyle.Render(chatWrap("", msg.Content, m.width))
b.WriteString(userBgStyle.Width(m.width).Render(rendered))

// New component approach
userMsg := NewMessageComponent("user", msg.Content).
    WithWidth(m.width).
    WithPadding(0, 1, 0, 1).
    WithStyle(userStyle)
b.WriteString(userMsg.Render())
```

## Implementation Plan

### Phase 1: Core Components
- [ ] Define Component interface
- [ ] Implement BaseComponent
- [ ] Create MessageComponent for each role
- [ ] Create StatusComponent

### Phase 2: Advanced Components
- [ ] Create HeaderComponent
- [ ] Create PaletteComponent
- [ ] Add composition utilities

### Phase 3: Refactoring
- [ ] Refactor render.go to use components
- [ ] Update theme defaults
- [ ] Add component-level tests

## Questions to Consider
1. Should components be mutable or immutable (return new instances)?
2. How should we handle dynamic content (streaming, thinking)?
3. Should we support component children for complex layouts?
4. How to integrate with existing zone/hover system?