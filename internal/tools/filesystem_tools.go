package tools

// FilesystemTool markers, grouped by migration state.
//
// Every tool that reads or writes files, or runs a process, claims
// FilesystemTool. The group it appears in says what must happen to it before an
// isolated workspace can be selected, and Registry.SetWorkspace computes its
// diagnostics from these markers.
//
// Moving a line from "needs migration" to "migrated" is what migrating a tool
// means: add a Workspace field, implement SetWorkspace, and route os.* and
// exec.Command through it.

// Migrated: filesystem and process operations route through a Workspace.
func (*ReadTool) filesystemTool()        {}
func (*WriteTool) filesystemTool()       {}
func (*EditTool) filesystemTool()        {}
func (*DeleteTool) filesystemTool()      {}
func (*PatchTool) filesystemTool()       {}
func (*BashTool) filesystemTool()        {}
func (*PowerShellTool) filesystemTool()  {}
func (*LsTool) filesystemTool()          {}
func (*FileInfoTool) filesystemTool()    {}
func (*GrepTool) filesystemTool()        {}
func (*GlobTool) filesystemTool()        {}
func (*JSONQueryTool) filesystemTool()   {}
func (*GoOutlineTool) filesystemTool()   {}
func (*ReplaceTool) filesystemTool()     {}
func (*GitTool) filesystemTool()         {}
func (*DiffTool) filesystemTool()        {}
func (*GoModTool) filesystemTool()       {}
func (*GoTestTool) filesystemTool()      {}
func (*StaticcheckTool) filesystemTool() {}
func (*BisectTool) filesystemTool()      {}

// Host-only by design: these belong to the host machine and cannot be isolated.
// They are not migration debt; registering one in a registry with an isolated
// workspace is a wiring error, which is why SetWorkspace reports them in a
// separate category.
//
//   - role manages sub-agent role files, which are harness configuration the host
//     reads, not workspace content;
//   - background_process drives the in-memory host process manager, and a
//     container would need its own;
//   - go_refactor reaches the host only for its dependency view: import
//     resolution in imports.Process and, for info, gc export data via
//     importer.Default() belong to the toolchain rather than to the project.
//     Every project file it reads or writes comes from the injected Workspace
//     (ws.ReadFile / ws.ReadDir), so what keeps it here is dependency metadata,
//     not source access.
func (*RoleTool) hostOnly()              {}
func (*BackgroundProcessTool) hostOnly() {}
func (*GoRefactorTool) hostOnly()        {}
