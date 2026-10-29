package subagent

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveWorld_Ladder(t *testing.T) {
	both := WorldAvailability{Container: true, ContainerOffered: true, ContainerRetains: true, Worktree: true, Jail: true}
	hostOnly := WorldAvailability{ContainerOffered: true, Worktree: true, Jail: true, ContainerReason: "containerd unreachable"}
	none := WorldAvailability{}

	tests := []struct {
		name      string
		req       WorldRequest
		av        WorldAvailability
		want      WorldKind
		degraded  bool
		wantErr   bool
		wantInErr string
	}{
		{
			name: "auto takes the container when available",
			req:  WorldRequest{Requested: "auto", Lane: "proposal", Retained: true},
			av:   both,
			want: WorldContainer,
		},
		{
			name: "auto steps down to a worktree and records why",
			req:  WorldRequest{Requested: "auto", Lane: "proposal", Retained: true},
			av:   hostOnly,
			want: WorldWorktree, degraded: true,
		},
		{
			name:    "container required fails closed",
			req:     WorldRequest{Requested: "container", Lane: "proposal"},
			av:      hostOnly,
			wantErr: true, wantInErr: "container required",
		},
		{
			name: "worktree request never takes a container",
			req:  WorldRequest{Requested: "worktree", Lane: "proposal"},
			av:   both,
			want: WorldWorktree,
		},
		{
			name:    "no world at all is an error, not a silent host run",
			req:     WorldRequest{Requested: "auto", Lane: "proposal"},
			av:      none,
			wantErr: true,
		},
		{
			name: "shared lane has no world",
			req:  WorldRequest{Requested: "auto", Lane: "shared"},
			av:   both,
			want: WorldShared,
		},
		{
			name:    "shared lane cannot request a container",
			req:     WorldRequest{Requested: "container", Lane: "shared"},
			av:      both,
			wantErr: true, wantInErr: "shared",
		},
		{
			name: "paddock cannot fall back to a jail-less worktree",
			req:  WorldRequest{Requested: "auto", Lane: "proposal", Containment: "paddock"},
			av:   WorldAvailability{Container: true, ContainerOffered: true, ContainerRetains: true, Worktree: true},
			want: WorldContainer,
		},
		{
			name:    "paddock fails closed when neither world is available",
			req:     WorldRequest{Requested: "auto", Lane: "proposal", Containment: "paddock"},
			av:      WorldAvailability{ContainerOffered: true, Worktree: true},
			wantErr: true,
		},
		{
			name:    "restrictive egress fails closed without a jail",
			req:     WorldRequest{Requested: "auto", Lane: "proposal", Network: "deny_all"},
			av:      WorldAvailability{ContainerOffered: true, Worktree: true},
			wantErr: true,
		},
		{
			name: "retained dispatch cannot use a non-settling container",
			req:  WorldRequest{Requested: "auto", Lane: "proposal", Retained: true},
			av:   WorldAvailability{Container: true, ContainerOffered: true, ContainerRetains: false, Worktree: true, Jail: true},
			want: WorldWorktree, degraded: true,
		},
		{
			name: "report dispatch may use a non-settling container",
			req:  WorldRequest{Requested: "auto", Lane: "report", Retained: false},
			av:   WorldAvailability{Container: true, ContainerOffered: true, ContainerRetains: false, Worktree: true, Jail: true},
			want: WorldContainer,
		},
		{
			name: "restrictive grants refuse a container that cannot lower them",
			req:  WorldRequest{Requested: "auto", Lane: "proposal", RestrictiveGrants: true},
			av:   both,
			want: WorldWorktree, degraded: true,
		},
		{
			name:    "unknown world is rejected",
			req:     WorldRequest{Requested: "podman", Lane: "proposal"},
			av:      both,
			wantErr: true, wantInErr: "unknown dispatch world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWorld(tt.req, tt.av)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveWorld() = %+v, want error", got)
				}
				if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantInErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWorld() error = %v", err)
			}
			if got.Resolved != tt.want {
				t.Fatalf("resolved = %q, want %q (%+v)", got.Resolved, tt.want, got)
			}
			if got.Degraded != tt.degraded {
				t.Fatalf("degraded = %v, want %v (%+v)", got.Degraded, tt.degraded, got)
			}
			if got.Degraded && got.Reason == "" {
				t.Fatal("a degradation must carry a reason")
			}
		})
	}
}

func TestResolveWorld_DegradeReasonNamesTheBlocker(t *testing.T) {
	got, err := ResolveWorld(
		WorldRequest{Requested: "auto", Lane: "proposal", Retained: true},
		WorldAvailability{Container: true, ContainerOffered: true, ContainerRetains: false, Worktree: true, Jail: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Reason, "settlement") {
		t.Fatalf("degradation reason %q must name the blocker", got.Reason)
	}
}

func TestResolveWorld_UnshippedContainerIsSilentFallback(t *testing.T) {
	// A build with no container lane promised nothing: auto must use the
	// worktree without a degradation warning (noise on every dispatch).
	got, err := ResolveWorld(
		WorldRequest{Requested: "auto", Lane: "proposal", Retained: true},
		WorldAvailability{ContainerOffered: false, Worktree: true, Jail: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolved != WorldWorktree || got.Degraded {
		t.Fatalf("got %+v, want an undegraded worktree", got)
	}
}

func TestResolveWorld_UnavailableIsTyped(t *testing.T) {
	_, err := ResolveWorld(
		WorldRequest{Requested: "container", Lane: "proposal"},
		WorldAvailability{Container: false, ContainerReason: "containerd unreachable"},
	)
	if !errors.Is(err, ErrWorldUnavailable) {
		t.Fatalf("error = %v, want ErrWorldUnavailable", err)
	}
}
