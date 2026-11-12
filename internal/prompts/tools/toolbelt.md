List and mount the session's toolbelt families — the tools folded out of the always-on set, which do not appear as schemas until mounted. `action: "list"` shows each family, its mount state, and full membership; `action: "mount"` registers a family for the rest of the session. Mount is sticky: there is no unmount, and remounting is a no-op. Use `mount` when a task needs one of a family's tools; prefer an already-available tool otherwise. A refused mount names the refusing stage (`persona`, `mode_floor`) — mount only what this session can call.

`action` is optional and defaults to `list`.
