package domain

import (
	"errors"
	"strings"
	"testing"
)

// A typo in an alert template used to survive compile and reach the operator
// verbatim: an alert reading "etl failed on {{taskk}}" is a silent misconfig
// discovered at the worst moment. Compile is the place to catch it.
func TestValidateAlertTemplates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		valid   bool
		wants   string
	}{
		{name: "empty is fine", message: "", valid: true},
		{name: "no placeholders", message: "something broke", valid: true},
		{name: "every known placeholder", valid: true,
			message: "{{dag}} {{run_id}} {{logical_date}} {{task}} {{tasks}}"},
		{name: "typo", message: "failed on {{taskk}}", valid: false, wants: "taskk"},
		{name: "airflow-style name we do not support", message: "{{ ds }}", valid: false, wants: "ds"},
		{name: "invented", message: "{{owner}} should look", valid: false, wants: "owner"},
		{name: "reports every unknown, not just the first", valid: false,
			message: "{{nope}} and {{alsonope}}", wants: "alsonope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &LeoflowConfig{Alerts: &AlertsConfig{OnFailure: []AlertRule{
				{Type: "slack", Conn: "c", Message: tc.message},
			}}}
			err := cfg.validateAlertTemplates()
			if tc.valid {
				if err != nil {
					t.Fatalf("validateAlertTemplates(%q) = %v, want nil", tc.message, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAlertTemplates(%q) = nil, want an error", tc.message)
			}
			if !errors.Is(err, ErrUnknownAlertPlaceholder) {
				t.Errorf("error does not wrap ErrUnknownAlertPlaceholder: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not name %q", err, tc.wants)
			}
			// The message must tell the author what they may use instead.
			if !strings.Contains(err.Error(), "{{dag}}") {
				t.Errorf("error %q does not list the supported placeholders", err)
			}
		})
	}
}

// The rule must survive a nil Alerts block and a config with no rules.
func TestValidateAlertTemplatesNoAlerts(t *testing.T) {
	if err := (&LeoflowConfig{}).validateAlertTemplates(); err != nil {
		t.Errorf("a config with no alerts should validate: %v", err)
	}
	if err := (&LeoflowConfig{Alerts: &AlertsConfig{}}).validateAlertTemplates(); err != nil {
		t.Errorf("an empty on_failure list should validate: %v", err)
	}
}
