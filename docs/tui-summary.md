# TUI Component System Summary

## What I've Proposed

Based on your request to treat the TUI like React with shared components and global properties like padding, I've designed and implemented a **component abstraction system** that addresses the repetitive and hard-to-maintain patterns in the current TUI code.

## Problem Identified

The current `render.go` has:
1. **Repetitive styling patterns** - Same padding/margin/width logic repeated across many functions
2. **Scattered style application** - Styles applied inline with manual calculations
3. **Hard to maintain** - Changes to global properties require updates in multiple places
4. **Inconsistent behavior** - Different components handle styling differently

## Solution: React-like Component System

### Core Components Created

1. **`component.go`** - Base interfaces and implementations:
   - `Component` interface with `Render()`, `WithWidth()`, `WithPadding()`, `WithMargin()`, `WithStyle()`
   - `BaseComponent` struct implementing common properties
   - Helper functions for padding/margin application

2. **`component_utils.go`** - Utility components:
   - `ComponentBuilder` - Fluent API for building components
   - `CompositeComponent` - Combines multiple components vertically
   - `ConditionalComponent` - Renders based on conditions
   - `ZoneComponent` - Wraps components with zone marking
   - `ExpandableComponent` - Provides expand/collapse functionality
   - `ComponentFactory` - Creates components with consistent styling

3. **`render_components.go`** - Refactored rendering:
   - `MessageComponentFactory` for message types
   - Refactored `renderMessages()` to use components
   - Global `ComponentStyle` defaults

4. **`view_components.go`** - Refactored View() method:
   - `ViewRefactored()` using component system
   - `ComponentView()` fully component-based approach
   - Palette components for overlays

### Key Benefits

1. **Centralized Styling**: Padding, margins, and width constraints defined once in `ComponentStyle`
2. **Consistent Behavior**: All components use the same patterns
3. **Easy Global Changes**: Modify `DefaultComponentStyle` affects all components
4. **Reusability**: Components can be composed and nested
5. **Maintainability**: Styling logic isolated from rendering logic
6. **Testability**: Components can be tested independently

## Example: Before vs After

### Before (Current Code)
```go
// Scattered throughout render.go
rendered := userStyle.Render(chatWrap("", msg.Content, m.width))
b.WriteString(userBgStyle.Width(m.width).Render(rendered))
```

### After (Component System)
```go
// Centralized component usage
userMsg := factory.CreateUserMessage(msg.Content)
rendered := userMsg.Render()
b.WriteString(userBgStyle.Width(m.width).Render(rendered))
```

## Migration Path

### Phase 1: Foundation (Week 1)
- Implement base component interfaces ✓
- Create message component factories ✓
- Refactor user/assistant message rendering

### Phase 2: Expansion (Week 2)
- Implement expandable components ✓
- Refactor tool message rendering
- Add component factory for consistency ✓

### Phase 3: Utilities (Week 3)
- Implement composite components ✓
- Add conditional/zone components ✓
- Create component builder API ✓

### Phase 4: Integration (Week 4)
- Refactor `renderMessages()` to use components
- Update `View()` method
- Add component-level tests

## Files Created

1. **`COMPONENT_DESIGN.md`** - Design document with rationale and migration plan
2. **`component.go`** - Core component interfaces and base implementation
3. **`component_utils.go`** - Utility components and factories
4. **`render_components.go`** - Refactored rendering functions
5. **`view_components.go`** - Refactored View() method
6. **`REFACTORING_EXAMPLE.md`** - Side-by-side comparison of current vs component approach

## Next Steps

1. **Test the component system** - Run existing tests to ensure no regressions
2. **Gradual migration** - Refactor one component type at a time
3. **Update theme system** - Integrate component styling with existing themes
4. **Add component tests** - Unit tests for component behavior
5. **Performance validation** - Ensure no performance regression

## Questions for You

1. **Immutability**: Should components return new instances (immutable) or modify in place (mutable)?
2. **Dynamic Content**: How should we handle streaming/thinking content in components?
3. **Integration**: Should we refactor the existing `View()` method or create a new one?
4. **Theme Integration**: How tightly should components integrate with the theme system?

## Impact Assessment

- **Low Risk**: Components are additive - can be adopted gradually
- **High Reward**: Significant reduction in code duplication
- **Maintainability**: Styling changes become trivial
- **Consistency**: All TUI elements behave the same way

The component system is ready for implementation. You can start using it immediately for new components or gradually refactor existing code.