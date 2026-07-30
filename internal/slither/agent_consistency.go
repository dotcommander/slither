package slither

import "context"

// verify confirms that a snapshot-backed operation did not cross a source
// change while it was reading source or preparing feedback. Filesystem scans
// have no cheap metadata witness, so they rebuild a deterministic report for
// the comparison; Git scans reuse the bounded fingerprint captured at build.
func (s *agentSnapshots) verify(ctx context.Context, report Report) error {
	if s.snapshot == nil || s.snapshot.report.ReportID != report.ReportID {
		return errAgentStaleEvidence
	}
	if report.SourceState.Kind == "git" {
		fingerprint, err := agentGitFingerprint(ctx, s.repo)
		if err != nil || fingerprint != s.snapshot.fingerprint {
			s.snapshot = nil
			return errAgentStaleEvidence
		}
		return nil
	}
	current, err := BuildReport(ctx, Options{Repo: s.repo, Top: defaultTop, MaxBytes: defaultMaxBytes, Days: defaultDays, NoCache: true})
	if err != nil {
		s.snapshot = nil
		return err
	}
	if current.ReportID != report.ReportID || current.SourceState.TreeDigest != report.SourceState.TreeDigest {
		s.snapshot = nil
		return errAgentStaleEvidence
	}
	return nil
}
