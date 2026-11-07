package main

import (
	"strings"
	"testing"
)

// The queue and scheduling pages use their existing save actions with an
// always-visible bottom bar. Retry-chain actions are deliberately unchanged.
func TestManagementSavebarsFloatOnlyOnQueueAndSettings(t *testing.T) {
	page := statusTemplateSource
	if got := strings.Count(page, "settings-savebar settings-savebar-floating"); got != 2 {
		t.Fatalf("expected exactly two floating savebars, got %d", got)
	}
	if got := strings.Count(page, "<div class=\"settings-savebar\">"); got != 1 {
		t.Fatalf("expected only retry to use the unchanged savebar, got %d", got)
	}
	for _, expected := range []string{
		".settings-savebar-floating{position:sticky;",
		"bottom:calc(12px + env(safe-area-inset-bottom, 0px))",
		".settings-savebar-floating .dirty-label",
		".workspace.workspace-with-floating-savebar",
		"'queue','settings'].includes(state.route)",
		"queue-save",
		"queue-revert",
		"export-settings",
		"import-settings",
		"save-settings",
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("floating-savebar expected markup or styling missing: %q", expected)
		}
	}
	if !strings.Contains(page, "settings-savebar-floating .toolbar-actions .btn") {
		t.Fatal("mobile floating savebar buttons must remain responsive")
	}
}
