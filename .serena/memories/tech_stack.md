# yaah — tech stack

- **Language**: Go 1.25.8 (toolchain go1.25.10)
- **Module**: `github.com/buchenberg/yaah`
- **CLI**: cobra + pflag (`github.com/spf13/cobra`)
- **Config**: `gopkg.in/yaml.v3` for `~/.yaah/config.yaml`
- **Database**: `modernc.org/sqlite` — pure Go, no CGo, FTS5 included
- **TUI**: `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `charm.land/glamour/v2` (markdown rendering)
- **Observability**: OpenTelemetry (`go.opentelemetry.io/otel`) — traces + metrics, OTLP HTTP exporters
- **Go analysis**: `golang.org/x/tools` (goimports, AST, package loading)
- **Banner**: `figlet-go` + lipgloss for TUI/REPL banner

## Key indirect deps

- `github.com/charmbracelet/x/ansi` — terminal ANSI handling
- `github.com/mattn/go-isatty` — TTY detection
- `google.golang.org/grpc` / `protobuf` — OTLP transport (transitive via OTel)