package tui

import "fmt"

// Header renders the top-of-screen title block: the ASCII art banner
// plus provider/model line when the banner is shown, or a compact
// one-line title when hidden.
type Header struct {
	banner     string
	provider   string
	model      string
	showBanner bool
}

// NewHeader creates a header component.
func NewHeader(banner, provider, model string, showBanner bool) Header {
	return Header{
		banner:     banner,
		provider:   provider,
		model:      model,
		showBanner: showBanner,
	}
}

// Render returns the header block.
func (h Header) Render() string {
	if h.showBanner && h.banner != "" {
		return h.banner + "\n\n" +
			titleStyle.Render(fmt.Sprintf("%s/%s", h.provider, h.model)) + "\n"
	}
	return titleStyle.Render(fmt.Sprintf("yaah · %s/%s", h.provider, h.model)) + "\n\n"
}
