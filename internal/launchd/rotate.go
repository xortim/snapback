package launchd

import (
	"fmt"
	"os"
)

const (
	// maxLogBytes is the size threshold at which a scheduled run's log
	// file gets rotated before this run writes anything else to it.
	maxLogBytes = 5 * 1024 * 1024
	// maxArchivedGenerations is how many rotated-away generations
	// (.log.1, .log.2) are kept alongside the live .log file -- 3
	// generations total, matching ADR-005.
	maxArchivedGenerations = 2
)

// RotateIfOversized renames path to path+".1" (after shifting any
// existing ".1".."maxArchivedGenerations-1" up by one and deleting
// anything at or beyond maxArchivedGenerations), if path's current size
// is at least maxLogBytes. A missing path is not an error -- there's
// nothing to rotate yet, e.g. this VM's first-ever scheduled run.
//
// Safe to call unconditionally at the top of every `run`, interactive or
// scheduled. For an interactive run (a real terminal, not launchd),
// rotating the on-disk file at this path has no effect on this
// process's own output, which goes to the terminal via
// cmd.OutOrStdout(), never to this path. For a launchd-invoked run, this
// process's stdout/stderr file descriptor was already dup2'd from this
// exact path by launchd before exec -- renaming the file now doesn't
// redirect *this* run's own output (it keeps writing into whatever
// inode it already has open, now named path+".1" after this call), but
// that's fine: launchd opens StandardOutPath fresh for every new
// process it spawns, so the *next* scheduled run gets a missing (and
// therefore freshly created, empty) file at path -- which is the actual
// goal. This run's own output ending up archived into path+".1" instead
// of a fresh path is an accepted, simple consequence of not needing any
// dup2/fd-reopening trickery to achieve it.
func RotateIfOversized(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() < maxLogBytes {
		return nil
	}

	oldest := fmt.Sprintf("%s.%d", path, maxArchivedGenerations)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", oldest, err)
	}
	for n := maxArchivedGenerations - 1; n >= 1; n-- {
		src := fmt.Sprintf("%s.%d", path, n)
		dst := fmt.Sprintf("%s.%d", path, n+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rename %s to %s: %w", src, dst, err)
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rename %s to %s.1: %w", path, path, err)
	}
	return nil
}
