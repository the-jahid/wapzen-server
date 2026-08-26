package models

import "testing"

// TestApplyDerivedRates pins the three rates to the counts they come from,
// including the case that matters most: a campaign that has done nothing reports
// zeroes rather than dividing by zero.
func TestApplyDerivedRates(t *testing.T) {
	tests := []struct {
		name                               string
		analytics                          CampaignAnalytics
		wantPickup, wantSuccess, wantReach float64
	}{
		{
			name:      "no calls and no leads stays at zero",
			analytics: CampaignAnalytics{},
		},
		{
			name: "rates come from the counts",
			analytics: CampaignAnalytics{
				Calls: CampaignCallTotals{Total: 96, Connected: 60, Successful: 58},
				Leads: CampaignLeadTotals{Total: 120, Contacted: 41},
			},
			wantPickup: 0.625, wantSuccess: 0.604, wantReach: 0.342,
		},
		{
			name: "leads without calls still report reach",
			analytics: CampaignAnalytics{
				Leads: CampaignLeadTotals{Total: 4, Contacted: 1},
			},
			wantReach: 0.25,
		},
		{
			name: "every call connected",
			analytics: CampaignAnalytics{
				Calls: CampaignCallTotals{Total: 2, Connected: 2, Successful: 2},
			},
			wantPickup: 1, wantSuccess: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.analytics
			got.ApplyDerivedRates()
			if got.PickupRate != tt.wantPickup {
				t.Errorf("pickup_rate = %v, want %v", got.PickupRate, tt.wantPickup)
			}
			if got.SuccessRate != tt.wantSuccess {
				t.Errorf("success_rate = %v, want %v", got.SuccessRate, tt.wantSuccess)
			}
			if got.ReachRate != tt.wantReach {
				t.Errorf("reach_rate = %v, want %v", got.ReachRate, tt.wantReach)
			}
		})
	}
}
