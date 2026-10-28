package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/retention"
)

var pruneNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

func ids(as []PrunedArchive) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.ArchiveID)
	}
	return out
}

func listedIDs(t *testing.T, destination string) []string {
	t.Helper()
	as, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives: %v", err)
	}
	var out []string
	for _, a := range as {
		out = append(out, a.ArchiveID)
	}
	return out
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection doesn't work as root")
	}
}

func TestPrune_RemovesBeyondPolicy_OnlyNamedVM(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	writeTestManifest(t, dest, "myvm-3", Manifest{VMName: "myvm", Timestamp: day(3)})
	writeTestManifest(t, dest, "other-1", Manifest{VMName: "other", Timestamp: day(1)})

	res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("Errs = %v, want none", res.Errs)
	}
	if got := ids(res.Kept); !slices.Equal(got, []string{"myvm-3"}) {
		t.Errorf("Kept = %v, want [myvm-3]", got)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-2", "myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-2 myvm-1]", got)
	}
	if got := listedIDs(t, dest); !slices.Equal(got, []string{"myvm-3", "other-1"}) {
		t.Errorf("listed after prune = %v, want [myvm-3 other-1]", got)
	}
	for _, id := range []string{"myvm-1", "myvm-2"} {
		if _, err := os.Stat(filepath.Join(dest, id)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (stat err = %v)", id, err)
		}
	}
}

func TestPrune_KeptCarriesReasons(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(28)})
	res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Kept) != 1 || !slices.Equal(res.Kept[0].Reasons, []string{"last"}) {
		t.Errorf("Kept = %+v, want one archive with reasons [last]", res.Kept)
	}
}

func TestPrune_PrefixNamedVMNotConsidered(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "ubuntu-20261001T000000Z", Manifest{VMName: "ubuntu", Timestamp: day(1)})
	writeTestManifest(t, dest, "ubuntu-dev-20261002T000000Z", Manifest{VMName: "ubuntu-dev", Timestamp: day(2)})
	writeTestManifest(t, dest, "ubuntu-dev-20261003T000000Z", Manifest{VMName: "ubuntu-dev", Timestamp: day(3)})

	res, err := Prune(t.Context(), dest, "ubuntu", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v, want none (ubuntu-dev archives are not ubuntu's)", ids(res.Removed))
	}
	if got := listedIDs(t, dest); len(got) != 3 {
		t.Errorf("listed after prune = %v, want all 3", got)
	}
}

func TestPrune_DirWithoutManifestSurvives(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	stray := filepath.Join(dest, "myvm-stray")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false); err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("manifest-less dir was touched: %v", err)
	}
}

func TestPrune_DryRunDeletesNothing(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})

	res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, true)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-1]", got)
	}
	if got := listedIDs(t, dest); len(got) != 2 {
		t.Errorf("listed after dry run = %v, want both", got)
	}
}

func TestPrune_ManifestRemoveFails_ArchiveIntactOthersStillRemoved(t *testing.T) {
	skipIfRoot(t)
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	writeTestManifest(t, dest, "myvm-3", Manifest{VMName: "myvm", Timestamp: day(3)})
	locked := filepath.Join(dest, "myvm-1")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Errs) != 1 || !strings.Contains(res.Errs[0].Error(), "myvm-1") {
		t.Errorf("Errs = %v, want one error naming myvm-1", res.Errs)
	}
	for _, r := range res.Removed {
		if failed := r.ArchiveID == "myvm-1"; failed != (r.Err != nil) {
			t.Errorf("Removed %s Err = %v, want non-nil only for myvm-1", r.ArchiveID, r.Err)
		}
	}
	if got := listedIDs(t, dest); !slices.Equal(got, []string{"myvm-3", "myvm-1"}) {
		t.Errorf("listed after prune = %v, want [myvm-3 myvm-1] (myvm-2 removed, myvm-1 intact)", got)
	}
}

func TestPrune_PartialRemoveAll_LeavesUnlistedDirAndReportsPath(t *testing.T) {
	skipIfRoot(t)
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	sub := filepath.Join(dest, "myvm-1", "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Errs) != 1 || !strings.Contains(res.Errs[0].Error(), filepath.Join(dest, "myvm-1")) {
		t.Errorf("Errs = %v, want one error naming %s", res.Errs, filepath.Join(dest, "myvm-1"))
	}
	if got := listedIDs(t, dest); !slices.Equal(got, []string{"myvm-2"}) {
		t.Errorf("listed after prune = %v, want [myvm-2] (myvm-1 unlisted once its manifest is gone)", got)
	}
}

func TestPrune_SymlinkedArchiveNotDeleted(t *testing.T) {
	dest := t.TempDir()
	target := t.TempDir()
	writeTestManifest(t, target, "real", Manifest{VMName: "myvm", Timestamp: day(1)})
	link := filepath.Join(dest, "myvm-1")
	if err := os.Symlink(filepath.Join(target, "real"), link); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})

	res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-1] (decided removed, even though not deleted)", got)
	}
	if len(res.Errs) != 1 || !errors.Is(res.Errs[0], ErrSymlinkedArchive) {
		t.Fatalf("Errs = %v, want one ErrSymlinkedArchive", res.Errs)
	}
	// The error has to tell the operator what to do: name the link's
	// target and the by-hand removal.
	for _, want := range []string{filepath.Join(target, "real"), "rm " + `"` + link + `"`, "rm -r"} {
		if !strings.Contains(res.Errs[0].Error(), want) {
			t.Errorf("symlink error = %q, want it to contain %q", res.Errs[0], want)
		}
	}
	if len(res.Removed) != 1 || res.Removed[0].Err == nil {
		t.Errorf("Removed = %+v, want the symlinked entry to carry Err", res.Removed)
	}
	for _, k := range res.Kept {
		if k.Err != nil {
			t.Errorf("Kept %s has Err = %v, want nil", k.ArchiveID, k.Err)
		}
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "real", "manifest.json")); err != nil {
		t.Errorf("symlink target's manifest removed: %v", err)
	}
}

func TestPrune_LockHeld_ReturnsErrLockedAndDeletesNothing(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	lock, err := AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	_, err = Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Prune() error = %v, want ErrLocked", err)
	}
	if got := listedIDs(t, dest); len(got) != 2 {
		t.Errorf("listed = %v, want both untouched", got)
	}
}

func TestPrune_MissingDestination_NoArchivesNoError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "never-created")
	res, err := pruneLocked(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("pruneLocked() error = %v, want nil", err)
	}
	if len(res.Kept)+len(res.Removed)+len(res.Errs) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
}

func TestPrune_MissingDestination_DoesNotCreateIt(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		dest := filepath.Join(t.TempDir(), "unmounted-drive")
		res, err := Prune(t.Context(), dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, dryRun)
		if err != nil {
			t.Fatalf("dryRun=%v: Prune() error = %v, want nil", dryRun, err)
		}
		if len(res.Kept)+len(res.Removed)+len(res.Errs) != 0 {
			t.Errorf("dryRun=%v: result = %+v, want empty", dryRun, res)
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("dryRun=%v: destination created (stat err = %v)", dryRun, err)
		}
	}
}

func TestPrune_CancelledContext_RemovesNothingReportsInterrupted(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := Prune(ctx, dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prune() error = %v, want context.Canceled", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v, want none after cancellation", ids(res.Removed))
	}
	got := listedIDs(t, dest)
	slices.Sort(got)
	if !slices.Equal(got, []string{"myvm-1", "myvm-2"}) {
		t.Errorf("listed = %v, want both untouched", got)
	}
}

func TestPrune_CancelledContext_DryRunUnaffected(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := Prune(ctx, dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, true)
	if err != nil {
		t.Fatalf("Prune() error = %v, want nil (dry run deletes nothing to interrupt)", err)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-1]", got)
	}
}
