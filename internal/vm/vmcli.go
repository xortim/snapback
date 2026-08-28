package vm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// vmcliCandidatePaths are checked, in order, when $SNAPBACK_VMCLI_PATH is
// unset and vmcli isn't on $PATH. vmcli lives under Fusion.app rather than
// a location a shell would resolve by default -- same reasoning as the
// vmrun $PATH gotcha in docs/design.md ("Risks & Gotchas").
var vmcliCandidatePaths = []string{
	"/Applications/VMware Fusion.app/Contents/Public/vmcli",
}

// findVMCLI locates the vmcli binary: $SNAPBACK_VMCLI_PATH override first,
// then the known Fusion install location, then $PATH.
func findVMCLI() (string, error) {
	if p := os.Getenv("SNAPBACK_VMCLI_PATH"); p != "" {
		return p, nil
	}
	for _, p := range vmcliCandidatePaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("vmcli"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("vmcli not found: checked $SNAPBACK_VMCLI_PATH, %v, and $PATH", vmcliCandidatePaths)
}

// VMCLIController is the real Controller, backed by vmcli (Fusion 13+).
// docs/design.md ("No library exists for snapshot operations") settled on
// vmcli over vmrun as the primary backing tool -- it has a dedicated
// Snapshot module and confirmed structured JSON output, where vmrun only
// offers plaintext. vmrun is not wired up here; it remains the documented
// fallback reference but isn't implemented (no current need has surfaced
// for a Fusion install new enough to lack vmcli).
type VMCLIController struct {
	// run executes `vmcli <args...>` and returns its stdout, stderr, and
	// error (non-nil on nonzero exit). A field rather than a direct
	// exec.Command call so tests can inject a fake.
	run func(args ...string) (stdout, stderr []byte, err error)
}

// NewVMCLIController locates vmcli and returns a Controller backed by it.
func NewVMCLIController() (*VMCLIController, error) {
	path, err := findVMCLI()
	if err != nil {
		return nil, err
	}
	return &VMCLIController{run: execVMCLI(path)}, nil
}

func execVMCLI(path string) func(args ...string) ([]byte, []byte, error) {
	return func(args ...string) ([]byte, []byte, error) {
		cmd := exec.Command(path, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
}

// vmcliError wraps a failed vmcli invocation, preferring vmcli's own
// stderr message (e.g. "VMX : 'x.vmx' does not exist!") over the bare
// "exit status 255" go's os/exec produces, since the former is what's
// actually actionable.
func vmcliError(action string, stderr []byte, err error) error {
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return fmt.Errorf("%s: %s", action, msg)
	}
	return fmt.Errorf("%s: %w", action, err)
}

type toolsQueryResult struct {
	Running       bool   `json:"running"`
	RunningStatus string `json:"runningStatus"`
	InstallType   string `json:"installType"`
}

// mapToolsState maps vmcli's `Tools Query -f json` output onto the four
// ToolsState constants. Confirmed empirically (docs/design.md) for the
// running+tools-installed case (installType "ovt", running true). The
// no-tools-installed case is not yet confirmed against vmcli's JSON shape
// on a real no-tools guest (only against vmrun's flatter checkToolsState
// string) -- both ToolsNotInstalled and this function's ToolsUnknown
// fallback are handled identically by the backup choreography (gate the
// quiesce step off, proceed crash-consistent), so under-distinguishing
// this case is safe, not just expedient. Revisit once the integration
// suite runs against a guest with no tools installed at all.
func mapToolsState(t toolsQueryResult) ToolsState {
	switch {
	case t.Running && t.RunningStatus == "running":
		return ToolsRunning
	case t.InstallType != "" && !strings.EqualFold(t.InstallType, "none"):
		return ToolsInstalled
	default:
		return ToolsUnknown
	}
}

func (c *VMCLIController) CheckToolsState(vmxPath string) (ToolsState, error) {
	stdout, stderr, err := c.run(vmxPath, "Tools", "Query", "-f", "json")
	if err != nil {
		return "", vmcliError("tools query", stderr, err)
	}
	var result toolsQueryResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return "", fmt.Errorf("parse tools query output: %w", err)
	}
	return mapToolsState(result), nil
}

type snapshotEntry struct {
	DisplayName string `json:"displayName"`
	UID         int64  `json:"uid"`
}

type snapshotQueryResult struct {
	Snapshots []snapshotEntry `json:"snapshots"`
}

func (c *VMCLIController) querySnapshots(vmxPath string) ([]snapshotEntry, error) {
	stdout, stderr, err := c.run(vmxPath, "Snapshot", "query", "-f", "json")
	if err != nil {
		return nil, vmcliError("snapshot query", stderr, err)
	}
	var result snapshotQueryResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return nil, fmt.Errorf("parse snapshot query output: %w", err)
	}
	return result.Snapshots, nil
}

// snapshotUID looks up name's uid via querySnapshots. vmcli's Snapshot
// Delete takes a uid, not a name (unlike vmrun) -- every delete goes
// through this lookup first. Display names are not guaranteed unique
// (e.g. an unretired orphan from a previous run sharing a same-second
// timestamp name with a new one), so more than one match is reported as
// an error rather than silently picking the first one.
func (c *VMCLIController) snapshotUID(vmxPath, name string) (uid int64, found bool, err error) {
	snapshots, err := c.querySnapshots(vmxPath)
	if err != nil {
		return 0, false, err
	}
	var matchUID int64
	matches := 0
	for _, s := range snapshots {
		if s.DisplayName == name {
			matchUID = s.UID
			matches++
		}
	}
	switch matches {
	case 0:
		return 0, false, nil
	case 1:
		return matchUID, true, nil
	default:
		return 0, false, fmt.Errorf("snapshot name %q is ambiguous: %d snapshots on %q share this display name", name, matches, vmxPath)
	}
}

// ErrOrphanPossible wraps the error Snapshot returns when a Take failure's
// cleanup could not be confirmed: the post-failure snapshot lookup itself
// failed or was ambiguous, or the cleanup Delete call failed. In every
// other failure case Snapshot's contract (below) still holds -- no
// snapshot was left behind. internal/backup.Run checks for this with
// errors.Is and tags its RunError at Stage: Snapshotting (instead of
// below it) so the caller's orphan warning fires.
var ErrOrphanPossible = errors.New("snapshot cleanup could not be confirmed; a partial snapshot may remain")

// Snapshot satisfies the Controller.Snapshot contract (internal/vm/controller.go):
// it must not leave a snapshot behind when it returns an error. vmcli's
// Take reports a single pass/fail, but if it fails after partially
// creating the snapshot, this checks for and removes it before returning.
// When that check or removal can't be confirmed, the returned error wraps
// ErrOrphanPossible instead of silently claiming success.
func (c *VMCLIController) Snapshot(vmxPath, name string) error {
	_, stderr, err := c.run(vmxPath, "Snapshot", "Take", "-d", "snapback automated snapshot", name)
	if err == nil {
		return nil
	}
	takeErr := vmcliError("snapshot", stderr, err)

	uid, found, qerr := c.snapshotUID(vmxPath, name)
	if qerr != nil {
		return fmt.Errorf("%w: could not verify whether a partial snapshot remains: %v (original failure: %v)", ErrOrphanPossible, qerr, takeErr)
	}
	if !found {
		return takeErr
	}

	if _, delStderr, delErr := c.run(vmxPath, "Snapshot", "Delete", strconv.FormatInt(uid, 10)); delErr != nil {
		cleanupErr := vmcliError("cleanup after failed snapshot", delStderr, delErr)
		return fmt.Errorf("%w: %v (original failure: %v)", ErrOrphanPossible, cleanupErr, takeErr)
	}
	return takeErr
}

func (c *VMCLIController) ListSnapshots(vmxPath string) ([]string, error) {
	snapshots, err := c.querySnapshots(vmxPath)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(snapshots))
	for i, s := range snapshots {
		names[i] = s.DisplayName
	}
	return names, nil
}

func (c *VMCLIController) DeleteSnapshot(vmxPath, name string) error {
	uid, found, err := c.snapshotUID(vmxPath, name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("snapshot %q not found for %q", name, vmxPath)
	}
	_, stderr, err := c.run(vmxPath, "Snapshot", "Delete", strconv.FormatInt(uid, 10))
	if err != nil {
		return vmcliError("delete snapshot", stderr, err)
	}
	return nil
}
