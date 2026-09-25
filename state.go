package main

import "time"

// retrySkippedAfter is how long messages that stayed in the source folder
// are left out before they are evaluated again.
const retrySkippedAfter = time.Hour

// state carries information from one run to the next.
type state struct {
	uidValidity    string
	fingerprint    string // hash of the config files
	missingFolders string

	// skipped holds UIDs that were evaluated but stayed in the source folder
	// (dry run, failed fetch or move). They are left out of the following
	// runs so they cannot block newer mail, and are retried after a while.
	skipped      map[string]bool
	skippedSince time.Time

	// copiedUnflagged holds UIDs that were copied to their destination but
	// whose original could not be flagged \Deleted. Processing them again
	// would copy them again, so they are only forgotten when the mailbox is
	// recreated (UIDVALIDITY) or the program restarts.
	copiedUnflagged map[string]bool

	warnedNoMove bool
}

func newState() *state {
	return &state{skipped: map[string]bool{}, copiedUnflagged: map[string]bool{}}
}

// sync forgets the skipped messages whenever evaluating them again could
// give a different result: the mailbox was recreated (UIDVALIDITY), a
// config file changed, a destination folder appeared or disappeared, or
// retrySkippedAfter has passed.
func (s *state) sync(uidValidity, fingerprint, missingFolders string, now time.Time) {
	if uidValidity != s.uidValidity {
		s.copiedUnflagged = map[string]bool{}
	}
	changed := uidValidity != s.uidValidity || fingerprint != s.fingerprint || missingFolders != s.missingFolders
	if changed || now.Sub(s.skippedSince) >= retrySkippedAfter {
		s.skipped = map[string]bool{}
		s.skippedSince = now
	}
	s.uidValidity, s.fingerprint, s.missingFolders = uidValidity, fingerprint, missingFolders
}

// pending returns the UIDs that were neither skipped nor copied without
// being flagged, in their original order.
func (s *state) pending(uids []string) []string {
	var result []string
	for _, uid := range uids {
		if !s.skipped[uid] && !s.copiedUnflagged[uid] {
			result = append(result, uid)
		}
	}
	return result
}

func (s *state) skip(uid string) {
	s.skipped[uid] = true
}

func (s *state) markCopiedUnflagged(uid string) {
	s.copiedUnflagged[uid] = true
}
