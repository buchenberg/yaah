# yaah — task completion checklist

When a coding task is considered done, run these in order:

## Quality gates

```powershell
# 1. Format check
gofmt -l .                          # must be empty

# 2. Build
go build -trimpath -ldflags '-s -w' -o yaah.exe .

# 3. Vet
go vet ./...

# 4. Tests
go test ./...

# 5. Static analysis
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

All must pass. Zero diagnostics is the goal.

## CI

CI runs on GitHub Actions (`.github/workflows/ci.yml`): test, vet, staticcheck, cross-compile.
PRs must be green before merging. Do not bump version in PRs — maintainers cut releases.