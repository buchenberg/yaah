package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
)

// container_inventory.go lists and prunes containers owned by the workspace
// sandbox layer. It deliberately understands both labels: croft renamed its
// container label from shepherd.sandbox to croft.sandbox, and a crash can leave
// containers from either version behind.
//
// Orphan means "owned by yaah but not running": no task, or a task that is
// stopped/created/unknown. A running or paused task is live work and is never
// offered for pruning. The caller must name the exact id to prune; the command
// re-checks the orphan classification instead of trusting the list it printed.

const (
	containerLabelCroft    = "croft.sandbox"
	containerLabelShepherd = "shepherd.sandbox"
)

// OrphanContainer is one non-running sandbox container.
type OrphanContainer struct {
	ID        string
	Image     string
	CreatedAt time.Time
}

// ListOrphanContainers returns non-running containers owned by the sandbox
// layer in namespace. A namespace is required because containerd isolates
// container ids per namespace.
func ListOrphanContainers(ctx context.Context, address, namespace string) ([]OrphanContainer, error) {
	cli, err := newContainerClient(address)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	ctx = namespaces.WithNamespace(ctx, containerNamespace(namespace))

	containers, err := cli.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var out []OrphanContainer
	for _, c := range containers {
		info, err := c.Info(ctx)
		if err != nil {
			// One unreadable container must not make the whole inventory
			// unusable; it is almost certainly not one of ours anyway.
			slog.Warn("container inventory: inspect failed", "container", c.ID(), "err", err)
			continue
		}
		if !ownedContainer(info.Labels) {
			continue
		}
		orphan, err := containerIsOrphan(ctx, c)
		if err != nil {
			slog.Warn("container inventory: task inspect failed", "container", c.ID(), "err", err)
			continue
		}
		if orphan {
			out = append(out, OrphanContainer{ID: c.ID(), Image: info.Image, CreatedAt: info.CreatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// PruneContainer deletes one orphaned sandbox container and its snapshot. It
// refuses any container that is not owned by the sandbox layer and any task
// that is still running or paused.
func PruneContainer(ctx context.Context, address, namespace, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("container id is required")
	}
	cli, err := newContainerClient(address)
	if err != nil {
		return err
	}
	defer cli.Close()
	ctx = namespaces.WithNamespace(ctx, containerNamespace(namespace))

	c, err := cli.LoadContainer(ctx, id)
	if err != nil {
		return fmt.Errorf("load container %s: %w", id, err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		return fmt.Errorf("inspect container %s: %w", id, err)
	}
	if !ownedContainer(info.Labels) {
		return fmt.Errorf("container %s is not owned by the yaah/croft sandbox layer", id)
	}
	// Load the task once: classify and delete the same handle rather than
	// re-reading it (a task could start between the two reads).
	task, err := c.Task(ctx, nil)
	if err == nil {
		status, serr := task.Status(ctx)
		if serr != nil {
			return fmt.Errorf("task status for %s: %w", id, serr)
		}
		if !taskStatusIsOrphan(status.Status) {
			return fmt.Errorf("container %s still has a live task (%s) — refusing to prune live work", id, status.Status)
		}
		if _, derr := task.Delete(ctx, containerd.WithProcessKill); derr != nil && !errdefs.IsNotFound(derr) {
			return fmt.Errorf("delete stopped task for %s: %w", id, derr)
		}
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("load task for %s: %w", id, err)
	}
	if err := c.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return fmt.Errorf("delete container %s: %w", id, err)
	}
	return nil
}

// containerIsOrphan reports whether the container has no live task. A task
// error other than not-found is returned: an unreadable task state must not be
// mistaken for an orphan.
func containerIsOrphan(ctx context.Context, c containerd.Container) (bool, error) {
	task, err := c.Task(ctx, nil)
	if errdefs.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	status, err := task.Status(ctx)
	if err != nil {
		return false, err
	}
	return taskStatusIsOrphan(status.Status), nil
}

// taskStatusIsOrphan is the pure classification used by both listing and
// pruning. Running and paused are live; everything else (created, stopped,
// unknown) is a leftover.
func taskStatusIsOrphan(status containerd.ProcessStatus) bool {
	switch status {
	case containerd.Running, containerd.Paused, containerd.Pausing:
		return false
	default:
		return true
	}
}

func ownedContainer(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	// A key alone is not ownership: an empty value is not a sandbox id.
	if v, ok := labels[containerLabelCroft]; ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := labels[containerLabelShepherd]; ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}

func containerNamespace(namespace string) string {
	if strings.TrimSpace(namespace) == "" {
		return "default"
	}
	return strings.TrimSpace(namespace)
}

func newContainerClient(address string) (*containerd.Client, error) {
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("containerd address is not configured (workspace.containerd.address)")
	}
	cli, err := containerd.New(strings.TrimSpace(address), containerd.WithTimeout(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect to containerd: %w", err)
	}
	return cli, nil
}
