package tools

// dispatch_container_live_test.go runs the per-dispatch container proposal
// lane against a real containerd daemon: resolve `world: container`, create a
// dispatch container, run the body inside it, seal a GitDelta manifest, destroy
// the container, then settle the manifest onto the host tree with `select`.
//
// It is inert unless the same knobs the session live test uses name a daemon
// and an image. Unlike TestLive_ActivatedSession it does not need root: an
// unprivileged user with access to the containerd socket (for example, the
// `containerd` group) is enough.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/config"
)

// TestLive_DispatchContainerProposal is the live acceptance test for the
// container proposal slice: a real dispatch container produces a durable
// manifest, the container is destroyed at seal, and settlement lands the
// change on the host.
func TestLive_DispatchContainerProposal(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv(liveAddrEnv))
	image := strings.TrimSpace(os.Getenv(liveImageEnv))
	if addr == "" || image == "" {
		t.Skipf("set %s and %s to run the dispatch-container live verification", liveAddrEnv, liveImageEnv)
	}

	timeout, err := time.ParseDuration(liveActivationEnv(liveTimeoutEnv, "10m"))
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Minute
	}

	cfg := config.WorkspaceConfig{
		Backend: config.BackendLocal,
		Root:    liveActivationEnv(liveRootEnv, "/workspace"),
		Containerd: config.ContainerdWorkspaceConfig{
			Address:     addr,
			Namespace:   liveActivationEnv(liveNamespaceEnv, "yaah-live"),
			Image:       image,
			Snapshotter: liveActivationEnv(liveSnapshotterEnv, ""),
			FIFODir:     filepath.Join(t.TempDir(), "fifo"),
		},
	}

	fx, repo, rootID := newSessionFixtures(t)
	run := func(ctx context.Context, _ string, params SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		if ws == nil {
			return "", errors.New("dispatch workspace missing from context")
		}
		if ws.Local() {
			return "", errors.New("dispatch workspace must not be the host tree")
		}
		if params.Workdir != ws.WorkDir() {
			return "", errors.New("sub-agent workdir differs from the dispatch workspace root")
		}
		if err := ws.WriteFile(ctx, "smoke.txt", []byte("hello from the container\n"), 0o644); err != nil {
			return "", err
		}
		return "wrote smoke.txt", nil
	}

	rt := RetainedRuntime{
		Runner:          run,
		RepoPath:        repo,
		ScopeManager:    fx.mgr,
		RootScopeID:     rootID,
		WorktreeRoot:    t.TempDir(),
		Proposals:       NewProposalRegistry(),
		World:           "container",
		ContainerWorlds: true,
		Containment:     "auto",
		Network:         "allow_all",
		DispatchWorlds: &DispatchWorlds{
			Config: cfg,
			Opener: OpenContainerdWorkspaceSandbox,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	envelope, err := startRetainedRun(ctx, rt, "write smoke.txt", SubAgentParams{Role: "developer"}, timeout, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if !strings.Contains(envelope, `"world":"container"`) {
		t.Fatalf("envelope does not name the container world: %s", envelope)
	}

	// Teardown failures are caveats on a still-proposal envelope, not errors:
	// a leaked container or stranded scope must fail this acceptance test.
	// An explicit container request never degrades (ResolveWorld fails closed
	// instead of returning a degraded container), so a warning cannot appear.
	var parsed struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Notes  string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(envelope), &parsed); err != nil {
		t.Fatalf("parse proposal envelope: %v", err)
	}
	if parsed.Status != "proposal" {
		t.Fatalf("envelope status = %q, want proposal (%s)", parsed.Status, envelope)
	}
	if parsed.Error != "" {
		t.Fatalf("dispatch reported an error: %s", parsed.Error)
	}
	if parsed.Notes != "" {
		t.Fatalf("dispatch teardown left a caveat (container or scope left behind): %s", parsed.Notes)
	}

	assertProposalFact(t, fx, rootID, SchemaProposalSealed)

	proposals := rt.Proposals.all()
	if len(proposals) != 1 {
		t.Fatalf("proposals = %d, want 1", len(proposals))
	}
	proposal := proposals[0]
	if proposal.delta == nil {
		t.Fatal("proposal has no GitDelta manifest")
	}
	if proposal.world != "container" {
		t.Errorf("proposal world = %q, want container", proposal.world)
	}
	if _, err := os.Stat(proposal.artifactDir); err != nil {
		t.Fatalf("manifest artifact dir %s: %v", proposal.artifactDir, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "smoke.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the host tree changed before settlement: %v", err)
	}

	if _, err := rt.Proposals.settleRetainedProposal(ctx, proposal.id, "select"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if _, err := os.Stat(proposal.artifactDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the artifact dir should be consumed by settlement; stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo, "smoke.txt"))
	if err != nil {
		t.Fatalf("read host smoke.txt: %v", err)
	}
	if string(got) != "hello from the container\n" {
		t.Errorf("host smoke.txt = %q", got)
	}
	assertProposalFact(t, fx, rootID, SchemaProposalSettled)
}

// assertProposalFact checks the durable causal record for one proposal fact on
// the session root's owner path. The live lane is the only place both the seal
// and settle facts are produced by the real dispatch path.
func assertProposalFact(t *testing.T, fx *sessionFixtures, rootID, schema string) {
	t.Helper()
	parent, ok := fx.mgr.Get(rootID)
	if !ok {
		t.Fatalf("root scope %s not found", rootID)
	}
	slice, err := fx.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, parent.OwnerID(), 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace owner %s: %v", parent.OwnerID(), err)
	}
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id]
		if !ok {
			continue
		}
		if rec.GetEnvelope().SchemaRef == schema {
			return
		}
	}
	t.Fatalf("no %s fact on the root owner path", schema)
}

// TestLive_ContainerSessionSiblingReport proves a sibling container in a
// container session is seeded from the session's captured state, not the host
// tree, and that the session container survives the dispatch.
func TestLive_ContainerSessionSiblingReport(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv(liveAddrEnv))
	image := strings.TrimSpace(os.Getenv(liveImageEnv))
	if addr == "" || image == "" {
		t.Skipf("set %s and %s to run the container-session sibling verification", liveAddrEnv, liveImageEnv)
	}

	timeout, err := time.ParseDuration(liveActivationEnv(liveTimeoutEnv, "10m"))
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cfg := config.WorkspaceConfig{
		Backend: config.BackendContainerd,
		Root:    liveActivationEnv(liveRootEnv, "/workspace"),
		Containerd: config.ContainerdWorkspaceConfig{
			Address:     addr,
			Namespace:   liveActivationEnv(liveNamespaceEnv, "yaah-live"),
			Image:       image,
			Snapshotter: liveActivationEnv(liveSnapshotterEnv, ""),
			FIFODir:     filepath.Join(t.TempDir(), "fifo"),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fx, repo, rootID := newSessionFixtures(t)
	session, err := StartSessionSandbox(ctx, cfg, OpenContainerdWorkspaceSandbox, repo, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		if derr := session.Destroy(dctx); derr != nil {
			t.Errorf("session destroy: %v", derr)
		}
	})
	if err := session.Workspace().WriteFile(ctx, "session-seed.txt", []byte("only in the session container\n"), 0o644); err != nil {
		t.Fatalf("seed session workspace: %v", err)
	}

	run := func(ctx context.Context, _ string, params SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		if ws == nil || ws.Local() {
			return "", errors.New("a sibling dispatch must run in a container workspace")
		}
		data, err := ws.ReadFile(ctx, "session-seed.txt")
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	rt := RetainedRuntime{
		Runner:          run,
		RepoPath:        repo,
		ScopeManager:    fx.mgr,
		RootScopeID:     rootID,
		WorktreeRoot:    t.TempDir(),
		Proposals:       NewProposalRegistry(),
		World:           "auto",
		ContainerWorlds: true,
		Containment:     "auto",
		Network:         "allow_all",
		DispatchWorlds: &DispatchWorlds{
			Config:         cfg,
			Opener:         OpenContainerdWorkspaceSandbox,
			SessionSandbox: session,
		},
	}

	got, err := startRetainedRun(ctx, rt, "read the session seed", SubAgentParams{Role: "reviewer"}, timeout, true)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if got != "only in the session container\n" {
		t.Fatalf("sibling report = %q, want the session container's state", got)
	}
	if len(rt.Proposals.all()) != 0 {
		t.Fatal("a report dispatch must not register a proposal")
	}
}
