package tui

// UserMessage renders a user chat message: bold user-colored text
// wrapped to the terminal width on the user background.
type UserMessage struct {
	content string
	width   int
}

// NewUserMessage creates a user message component.
func NewUserMessage(content string, width int) UserMessage {
	return UserMessage{content: content, width: width}
}

// Render returns the wrapped, styled user message with a trailing newline.
func (c UserMessage) Render() string {
	return chatBubble(c.content, c.width, userStyle, userBgStyle) + "\n"
}

// AssistantMessage renders assistant chat content in the assistant
// foreground color. Trailing newlines are handled by the caller so the
// component composes with reasoning sections and streaming output.
type AssistantMessage struct {
	content string
}

// NewAssistantMessage creates an assistant message component.
func NewAssistantMessage(content string) AssistantMessage {
	return AssistantMessage{content: content}
}

// Render returns the styled assistant content.
func (c AssistantMessage) Render() string {
	return assistantStyle.Render(c.content)
}

// SubAgentBracket renders the box-drawing corners that bracket a
// sub-agent's lifetime in the chat: ╭─ opens the block (bold) and
// ╰─ closes it (plain), both in the tool color. The corners frame the
// sub-agent's tool output between them like a container.
type SubAgentBracket struct {
	label string
	start bool
}

// NewSubAgentBracket creates a sub-agent bracket component.
// start=true renders the opening (bold) corner; false the closing one.
func NewSubAgentBracket(label string, start bool) SubAgentBracket {
	return SubAgentBracket{label: label, start: start}
}

// Render returns the styled bracket with a trailing newline.
func (c SubAgentBracket) Render() string {
	if c.start {
		return subAgentStartStyle.Render("╭─ sub-agent: "+c.label) + "\n"
	}
	return subAgentEndStyle.Render("╰─ sub-agent: "+c.label) + "\n"
}

// SystemMessage renders a system or other-role message: wrapped text
// in the system foreground on the system background.
type SystemMessage struct {
	content string
	width   int
}

// NewSystemMessage creates a system message component.
func NewSystemMessage(content string, width int) SystemMessage {
	return SystemMessage{content: content, width: width}
}

// Render returns the wrapped, styled system message with a trailing newline.
func (c SystemMessage) Render() string {
	return chatBubble(c.content, c.width, systemStyle, systemBgStyle) + "\n"
}
