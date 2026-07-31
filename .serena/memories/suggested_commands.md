# yaah — suggested commands

All commands run from project root. **Default shell is PowerShell (pwsh).**

## Build

```powershell
go build -trimpath -ldflags '-s -w' -o yaah.exe .
```

## Test

```powershell
go test ./...
```

## Lint & vet

```powershell
go vet ./...
gofmt -l .                              # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

## Cross-compile matrix

```powershell
$targets = @(
  @{GOOS="darwin";  GOARCH="arm64"; Out="dist/yaah-darwin-arm64"},
  @{GOOS="darwin";  GOARCH="amd64"; Out="dist/yaah-darwin-amd64"},
  @{GOOS="linux";   GOARCH="amd64"; Out="dist/yaah-linux-amd64"},
  @{GOOS="linux";   GOARCH="arm64"; Out="dist/yaah-linux-arm64"},
  @{GOOS="windows"; GOARCH="amd64"; Out="dist/yaah-windows-amd64.exe"}
)
foreach ($t in $targets) {
  $env:GOOS=$t.GOOS; $env:GOARCH=$t.GOARCH
  go build -trimpath -ldflags '-s -w' -o $t.Out .
}
```

## Windows-specific notes

- Use `ditto --norsrc` (macOS) for install; on Windows, just copy `yaah.exe`
- `Get-Process yaah` / `Stop-Process` for process management
- PowerShell is the default shell; use `powershell` tool for all commands
- Path separator is `\` but Go handles `/` fine in most contexts

## Dev loop (MCP hot-reload)

```powershell
go build -o yaah.exe .
Get-Process yaah -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Process ./yaah.exe -ArgumentList 'serve','--http','127.0.0.1:7333' -NoNewWindow
```

Full instructions in `.agents/skills/yaah-dev-loop/SKILL.md`.