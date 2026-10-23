package agents

import (
	"encoding/json"
	"testing"
)

func TestManualPlanAuditWaitsForUserWhenConflictsAreReported(t *testing.T) {
	result := json.RawMessage(`{"foundation_ready":false,"user_review_required":true}`)
	if !foundationReadyResult("audit_foundation", result) || !architectLongShouldStopAfterToolResult("audit_foundation", result) {
		t.Fatal("a completed manual conflict report must end the audit and wait for user correction")
	}
	if foundationReadyResult("audit_foundation", json.RawMessage(`{"foundation_ready":false}`)) {
		t.Fatal("ordinary model planning should continue fixing its audit issues")
	}
}
