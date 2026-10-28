package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/retention"
)

var pruneTestNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func writePruneArchive(t *testing.T, dest, id, vmName string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(dest, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(backup.Manifest{VMName: vmName, Timestamp: ts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func execPrune(t *testing.T, deps pruneDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := swapSubcommand(t, "prune", newPruneCmdWithDeps(deps))
	var out, errOut bytes.Buffer
	root.SetArgs(append([]string{"prune"}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func realPruneDeps(cfg *config.Config) pruneDeps {
	return pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune:      backup.Prune,
		now:        func() time.Time { return pruneTestNow },
	}
}

func TestPruneCmd_AllVMs_RemovesAndReports(t *testing.T) {
	dest := t.TempDir()
	writePruneArchive(t, dest, "a-1", "a", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "a-2", "a", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-1", "b", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-2", "b", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	cfg := &config.Config{Destination: dest, Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}

	out, _, err := execPrune(t, realPruneDeps(cfg))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"a-2", "keep (last)", "a-1", "remove", "b-1", "b-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	for _, id := range []string{"a-1", "b-1"} {
		if _, err := os.Stat(filepath.Join(dest, id)); !os.IsNotExist(err) {
			t.Errorf("%s still present (err = %v)", id, err)
		}
	}
}

func TestPruneCmd_VMFlag_OnlyThatVM(t *testing.T) {
	dest := t.TempDir()
	writePruneArchive(t, dest, "a-1", "a", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "a-2", "a", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-1", "b", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-2", "b", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	cfg := &config.Config{Destination: dest, Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}

	if _, _, err := execPrune(t, realPruneDeps(cfg), "--vm", "a"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a-1")); !os.IsNotExist(err) {
		t.Errorf("a-1 still present")
	}
	if _, err := os.Stat(filepath.Join(dest, "b-1")); err != nil {
		t.Errorf("b-1 removed despite --vm a: %v", err)
	}
}

func TestPruneCmd_UnknownVM_Errors(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "a"}}}
	_, _, err := execPrune(t, realPruneDeps(cfg), "--vm", "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("Execute() error = %v, want an unknown-VM error naming nope", err)
	}
}

func TestPruneCmd_DryRun_DeletesNothing(t *testing.T) {
	dest := t.TempDir()
	writePruneArchive(t, dest, "a-1", "a", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "a-2", "a", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	cfg := &config.Config{Destination: dest, Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}

	out, _, err := execPrune(t, realPruneDeps(cfg), "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "would remove") {
		t.Errorf("stdout = %q, want 'would remove'", out)
	}
	if _, err := os.Stat(filepath.Join(dest, "a-1")); err != nil {
		t.Errorf("dry run deleted a-1: %v", err)
	}
}

func TestPruneCmd_LockedVM_SkippedNotFailed(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}
	var pruned []string
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(_ context.Context, dest, vmName string, p retention.Policy, now time.Time, dryRun bool) (backup.PruneResult, error) {
			if vmName == "a" {
				return backup.PruneResult{}, fmt.Errorf("%w: %q", backup.ErrLocked, vmName)
			}
			pruned = append(pruned, vmName)
			return backup.PruneResult{}, nil
		},
		now: func() time.Time { return pruneTestNow },
	}

	out, _, err := execPrune(t, deps)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (locked VM is skipped, not failed)", err)
	}
	if !strings.Contains(out, `backup for "a" may be in progress (lock held); skipping`) {
		t.Errorf("stdout = %q, want a skip notice for a", out)
	}
	if len(pruned) != 1 || pruned[0] != "b" {
		t.Errorf("pruned = %v, want [b]", pruned)
	}
}

func TestPruneCmd_RemovalError_NonZeroExit(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(context.Context, string, string, retention.Policy, time.Time, bool) (backup.PruneResult, error) {
			return backup.PruneResult{Errs: []error{errBoom}}, nil
		},
		now: func() time.Time { return pruneTestNow },
	}

	_, stderr, err := execPrune(t, deps)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for a removal error")
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want the removal error printed", stderr)
	}
}

func TestPruneCmd_NoArchives_ReportsNothingToPrune(t *testing.T) {
	cfg := &config.Config{Destination: filepath.Join(t.TempDir(), "never-created"), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}
	out, _, err := execPrune(t, realPruneDeps(cfg))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, `"a": no archives`) {
		t.Errorf("stdout = %q, want a no-archives line for a", out)
	}
}

func TestPruneCmd_RemovalFailure_ShowsRemoveFailed(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}
	arch := func(id string, e error) backup.PrunedArchive {
		return backup.PrunedArchive{Archive: backup.Archive{ArchiveID: id, Manifest: backup.Manifest{VMName: "a", Timestamp: pruneTestNow}}, Err: e}
	}
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(context.Context, string, string, retention.Policy, time.Time, bool) (backup.PruneResult, error) {
			return backup.PruneResult{
				Kept:    []backup.PrunedArchive{arch("a-3", nil)},
				Removed: []backup.PrunedArchive{arch("a-1", nil), arch("a-2", errBoom)},
				Errs:    []error{errBoom},
			}, nil
		},
		now: func() time.Time { return pruneTestNow },
	}

	out, _, err := execPrune(t, deps)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for a removal failure")
	}
	for _, want := range []string{"remove failed", "kept 1, removed 1, failed 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
}

func TestPruneCmd_Interrupted_StopsAndReports(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}
	var pruned []string
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(_ context.Context, _, vmName string, _ retention.Policy, _ time.Time, _ bool) (backup.PruneResult, error) {
			pruned = append(pruned, vmName)
			done := backup.PrunedArchive{Archive: backup.Archive{ArchiveID: "a-1"}}
			return backup.PruneResult{Removed: []backup.PrunedArchive{done}}, fmt.Errorf("prune interrupted: %w", context.Canceled)
		},
		now: func() time.Time { return pruneTestNow },
	}

	_, stderr, err := execPrune(t, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if len(pruned) != 1 || pruned[0] != "a" {
		t.Errorf("pruned = %v, want [a] only (stop after interruption)", pruned)
	}
	if !strings.Contains(stderr, `prune "a" interrupted after removing 1 archive(s)`) {
		t.Errorf("stderr = %q, want an interrupted notice", stderr)
	}
}

func TestPruneCmd_SymlinkFailure_LabelledInTable(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}
	symErr := fmt.Errorf("prune x: %w to y", backup.ErrSymlinkedArchive)
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(context.Context, string, string, retention.Policy, time.Time, bool) (backup.PruneResult, error) {
			a := backup.PrunedArchive{Archive: backup.Archive{ArchiveID: "a-1", Manifest: backup.Manifest{VMName: "a", Timestamp: pruneTestNow}}, Err: symErr}
			return backup.PruneResult{Removed: []backup.PrunedArchive{a}, Errs: []error{symErr}}, nil
		},
		now: func() time.Time { return pruneTestNow },
	}

	out, _, err := execPrune(t, deps)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if !strings.Contains(out, "remove failed (symlink, see below)") {
		t.Errorf("stdout = %q, want the symlink failure labelled", out)
	}
}
