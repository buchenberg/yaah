package tools

import (
	"testing"

	containerd "github.com/containerd/containerd/v2/client"
)

func TestTaskStatusIsOrphan(t *testing.T) {
	for _, tc := range []struct {
		status containerd.ProcessStatus
		want   bool
	}{
		{containerd.Running, false},
		{containerd.Paused, false},
		{containerd.Stopped, true},
		{containerd.Created, true},
		{containerd.Unknown, true},
	} {
		if got := taskStatusIsOrphan(tc.status); got != tc.want {
			t.Errorf("taskStatusIsOrphan(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestOwnedContainer(t *testing.T) {
	if ownedContainer(nil) {
		t.Error("a container with no labels is not ours")
	}
	if !ownedContainer(map[string]string{containerLabelCroft: "id"}) {
		t.Error("croft.sandbox is an owned label")
	}
	if !ownedContainer(map[string]string{containerLabelShepherd: "id"}) {
		t.Error("shepherd.sandbox is a legacy owned label")
	}
	if ownedContainer(map[string]string{"other": "x"}) {
		t.Error("an unrelated label is not ownership")
	}
}
