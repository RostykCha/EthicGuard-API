package analysis

import "testing"

func TestDecide(t *testing.T) {
	longAC := "Given the user is logged in, when they click submit, then the order is created and confirmation is shown."
	cases := []struct {
		name    string
		acText  string
		hasTest bool
		finds   []Finding
		want    Decision
	}{
		{
			name:    "empty AC -> not ready",
			acText:  "",
			hasTest: true,
			finds:   nil,
			want:    Decision{Primary: LabelACNotReady, NoTest: false},
		},
		{
			name:    "very short AC -> not ready, no test stamp",
			acText:  "tbd",
			hasTest: false,
			finds:   nil,
			want:    Decision{Primary: LabelACNotReady, NoTest: true},
		},
		{
			name:    "high severity finding -> defect",
			acText:  longAC,
			hasTest: true,
			finds:   []Finding{{Category: "ambiguity", Severity: SeverityHigh}},
			want:    Decision{Primary: LabelACDefect, NoTest: false},
		},
		{
			name:    "only medium severity findings -> verified",
			acText:  longAC,
			hasTest: true,
			finds:   []Finding{{Category: "ambiguity", Severity: SeverityMedium}},
			want:    Decision{Primary: LabelACVerified, NoTest: false},
		},
		{
			name:    "no findings, has test links -> verified",
			acText:  longAC,
			hasTest: true,
			finds:   nil,
			want:    Decision{Primary: LabelACVerified, NoTest: false},
		},
		{
			name:    "no findings, no test links -> verified + no test",
			acText:  longAC,
			hasTest: false,
			finds:   nil,
			want:    Decision{Primary: LabelACVerified, NoTest: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.finds, &IssuePayload{
				AcceptanceCriteria: tc.acText,
				HasTestLinks:       tc.hasTest,
			})
			if got != tc.want {
				t.Errorf("Decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecisionLabels(t *testing.T) {
	d := Decision{Primary: LabelACVerified, NoTest: true}
	got := d.Labels()
	if len(got) != 2 || got[0] != LabelACVerified || got[1] != LabelNoTest {
		t.Errorf("Labels = %v, want [AC verified, no test]", got)
	}

	d = Decision{Primary: LabelACDefect, NoTest: false}
	got = d.Labels()
	if len(got) != 1 || got[0] != LabelACDefect {
		t.Errorf("Labels = %v, want [AC defect]", got)
	}
}

func TestFilterBySeverity(t *testing.T) {
	all := []Finding{
		{Severity: SeverityInfo},
		{Severity: SeverityLow},
		{Severity: SeverityMedium},
		{Severity: SeverityHigh},
	}
	cases := []struct {
		name      string
		threshold string
		wantLen   int
	}{
		{"empty threshold passes everything", "", 4},
		{"info keeps everything", SeverityInfo, 4},
		{"low drops info", SeverityLow, 3},
		{"medium drops info+low", SeverityMedium, 2},
		{"high keeps only high", SeverityHigh, 1},
		{"unknown threshold is treated as no-op", "critical", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterBySeverity(all, tc.threshold)
			if len(got) != tc.wantLen {
				t.Errorf("FilterBySeverity(%q) returned %d findings, want %d", tc.threshold, len(got), tc.wantLen)
			}
		})
	}
}

func TestSeverityAtLeast(t *testing.T) {
	cases := []struct {
		name      string
		s         Severity
		threshold Severity
		want      bool
	}{
		{"high meets high", Severity(SeverityHigh), Severity(SeverityHigh), true},
		{"high meets medium", Severity(SeverityHigh), Severity(SeverityMedium), true},
		{"medium below high", Severity(SeverityMedium), Severity(SeverityHigh), false},
		{"low meets info", Severity(SeverityLow), Severity(SeverityInfo), true},
		{"info below low", Severity(SeverityInfo), Severity(SeverityLow), false},
		{"info meets info", Severity(SeverityInfo), Severity(SeverityInfo), true},
		{"unknown s rejected", Severity("critical"), Severity(SeverityHigh), false},
		{"unknown threshold rejected", Severity(SeverityHigh), Severity("none"), false},
		{"both unknown rejected", Severity("a"), Severity("b"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.AtLeast(tc.threshold); got != tc.want {
				t.Errorf("Severity(%q).AtLeast(%q) = %v, want %v",
					tc.s, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestMessageCatalog(t *testing.T) {
	// Sanity check: every category × severity combo the LLM is allowed to
	// emit must have a catalog entry. If you add a new category to the system
	// prompt, also add entries here.
	categories := []string{"ambiguity", "missing_edge_case", "missing_negative_case", "untestable", "incomplete"}
	severities := []string{"high", "medium", "low", "info"}
	for _, c := range categories {
		for _, s := range severities {
			key := MessageKey(c, s)
			if _, ok := messageCatalog[key]; !ok {
				t.Errorf("catalog missing entry for %q", key)
			}
		}
	}
}
