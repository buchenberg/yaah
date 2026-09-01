---
name: dolt-server-lifecycle
description: Use when bd (beads) is configured in --server mode and needs a local dolt sql-server running. Covers starting/stopping/persisting a Dolt SQL server on Windows for use as a bd backend, symptoms of a down server, standard yaah local layout, and version gotchas (Dolt 2.x flag removals).
---

# Dolt server lifecycle for bd (Windows)

Applies when `bd` is configured in `--server` mode (i.e. `.beads/config.yaml` has `storage.type: server`) and needs a running `dolt sql-server` to reach. This is the default on Windows after switching away from embedded-Dolt mode, because a CGO-disabled `bd.exe` cannot embed Dolt.

## When bd complains

Symptoms:
- `bd status` → `Error: connect: connection refused`
- `bd list` → hangs then times out
- `bd dolt show` → shows configured host/port but no version

Fix: (re)start the local Dolt SQL server.

## Standard local layout (this machine)

- Dolt binary: `C:\Program Files\Dolt\bin\dolt.exe` (winget: `DoltHub.Dolt`)
- Server data dir: `~/.dolt-servers/yaah/` (contains `.dolt/` + `yaah` database)
- Server address: `127.0.0.1:3306`, user `root`, no password
- Config: `.beads/config.yaml` in the project points here via `storage.server`

## Manual start (foreground, one shell)

```powershell
$env:PATH = "C:\Program Files\Dolt\bin;$env:PATH"
Set-Location "$HOME\.dolt-servers\yaah"
dolt sql-server --host 127.0.0.1 --port 3306
```

Leave the window open. `Ctrl+C` stops it.

## Manual start (background)

```powershell
$env:PATH = "C:\Program Files\Dolt\bin;$env:PATH"
Start-Process -FilePath "dolt" `
  -ArgumentList "sql-server","--host","127.0.0.1","--port","3306" `
  -WorkingDirectory "$HOME\.dolt-servers\yaah" `
  -WindowStyle Hidden
```

## Verify it's up

```powershell
# TCP listener present?
Get-NetTCPConnection -LocalPort 3306 -State Listen -ErrorAction SilentlyContinue

# bd can reach it?
bd status
```

## Persistent start at logon (Scheduled Task, recommended)

Run once, from an elevated PowerShell:

```powershell
$action = New-ScheduledTaskAction `
  -Execute "C:\Program Files\Dolt\bin\dolt.exe" `
  -Argument "sql-server --host 127.0.0.1 --port 3306" `
  -WorkingDirectory "$HOME\.dolt-servers\yaah"
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName "dolt-sql-server-bd" `
  -Action $action -Trigger $trigger -Settings $settings `
  -RunLevel Highest -Description "Local Dolt SQL server for bd (beads)"
```

To remove: `Unregister-ScheduledTask -TaskName dolt-sql-server-bd -Confirm:$false`.

## Version gotchas

- Dolt 2.x removed `--user` and `--password` flags from `sql-server`. Passing them causes a silent crash at startup — the server appears to launch but the port never opens. If a background start "succeeded" but bd can't reach 3306, check the process logs first.
- `dolt sql-server` locks the data directory. A second instance targeting the same dir will fail. Check with `Get-Process dolt` before starting.

## Backup before schema changes

The `.beads/` dir on disk is a passive JSONL export, not the source of truth. The real DB lives in `~/.dolt-servers/yaah/.dolt/`. Snapshot before any risky migration:

```powershell
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
Copy-Item -Recurse "$HOME\.dolt-servers\yaah" "$HOME\.dolt-servers\yaah.backup-$stamp"
```

## Related

- Root-cause fix (not covered here): rebuild `bd` with `CGO_ENABLED=1` to run embedded Dolt again and drop the sidecar server. Requires a C toolchain.
- Skill `beads` covers the `bd` CLI workflow. This skill covers only the Dolt-server dependency.

