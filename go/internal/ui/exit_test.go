// Tripwire for the exit-code surface. If a number here drifts away
// from what SPEC.md §8 documents, this test fails and the discrepancy
// surfaces in CI before users see it.
//
// The test deliberately re-encodes the spec table as Go data rather
// than parsing the markdown — we want a *second* assertion of the
// expected codes that lives in code, so accidentally changing
// `ExitInitdbFailed = 11` to `12` requires editing both places.

package ui

import "testing"

func TestExitCodesMatchSpec(t *testing.T) {
	cases := []struct {
		name string
		code ExitCode
		want int
	}{
		{"ExitOK", ExitOK, 0},
		{"ExitGeneric", ExitGeneric, 1},
		{"ExitUsage", ExitUsage, 2},
		{"ExitNotASandbox", ExitNotASandbox, 3},
		{"ExitNotACluster", ExitNotACluster, 4},
		{"ExitSandboxExists", ExitSandboxExists, 5},
		{"ExitClusterExists", ExitClusterExists, 6},
		{"ExitBadConfig", ExitBadConfig, 7},
		{"ExitConfigKeyUnknown", ExitConfigKeyUnknown, 8},
		{"ExitPortInUse", ExitPortInUse, 9},
		{"ExitNoFreePort", ExitNoFreePort, 10},
		{"ExitInitdbFailed", ExitInitdbFailed, 11},
		{"ExitPgctlFailed", ExitPgctlFailed, 12},
		{"ExitBasebackupFailed", ExitBasebackupFailed, 13},
		{"ExitSourceUnreachable", ExitSourceUnreachable, 14},
		{"ExitPublicationFailed", ExitPublicationFailed, 15},
		{"ExitSubscriptionFailed", ExitSubscriptionFailed, 16},
		{"ExitSchemaCopyFailed", ExitSchemaCopyFailed, 17},
		{"ExitNotAStandby", ExitNotAStandby, 18},
		{"ExitPromoteFailed", ExitPromoteFailed, 19},
		{"ExitDestroyFailed", ExitDestroyFailed, 20},
		{"ExitClusterDeployFailed", ExitClusterDeployFailed, 21},
		{"ExitClusterDestroyPartial", ExitClusterDestroyPartial, 22},
		{"ExitPgGatherDirMissing", ExitPgGatherDirMissing, 23},
		{"ExitReportFailed", ExitReportFailed, 24},
		{"ExitPsqlFailed", ExitPsqlFailed, 25},
		{"ExitInterrupted", ExitInterrupted, 26},
		{"ExitNotATTY", ExitNotATTY, 27},
		{"ExitRestartRequiredRefused", ExitRestartRequiredRefused, 28},
		{"ExitBuildFailed", ExitBuildFailed, 30},
	}
	for _, tc := range cases {
		if tc.code.Int() != tc.want {
			t.Errorf("%s: got %d, want %d (SPEC.md §8 mismatch)", tc.name, tc.code.Int(), tc.want)
		}
	}
}
