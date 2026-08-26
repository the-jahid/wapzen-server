package models

import (
	"testing"
	"time"
)

func TestCanTransitionCampaignStatus(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{name: "draft starts", from: CampaignStatusDraft, to: CampaignStatusRunning, want: true},
		{name: "draft fails", from: CampaignStatusDraft, to: CampaignStatusFailed, want: true},
		{name: "draft cannot complete without running", from: CampaignStatusDraft, to: CampaignStatusCompleted, want: false},
		{name: "draft cannot pause", from: CampaignStatusDraft, to: CampaignStatusPaused, want: false},
		{name: "running pauses", from: CampaignStatusRunning, to: CampaignStatusPaused, want: true},
		{name: "running completes", from: CampaignStatusRunning, to: CampaignStatusCompleted, want: true},
		{name: "running cannot go back to draft", from: CampaignStatusRunning, to: CampaignStatusDraft, want: false},
		{name: "paused resumes", from: CampaignStatusPaused, to: CampaignStatusRunning, want: true},
		{name: "paused completes", from: CampaignStatusPaused, to: CampaignStatusCompleted, want: true},
		{name: "completed is terminal", from: CampaignStatusCompleted, to: CampaignStatusRunning, want: false},
		{name: "failed is terminal", from: CampaignStatusFailed, to: CampaignStatusRunning, want: false},
		{name: "no-op transition allowed", from: CampaignStatusCompleted, to: CampaignStatusCompleted, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransitionCampaignStatus(tt.from, tt.to); got != tt.want {
				t.Errorf("CanTransitionCampaignStatus(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

// TestApplyDerivedFieldsRates pins the two rates to the counters they come
// from, including the zero-call case that would otherwise be a division by zero.
func TestApplyDerivedFieldsRates(t *testing.T) {
	today := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		campaign        OutboundCampaign
		wantPickupRate  float64
		wantSuccessRate float64
	}{
		{
			name:            "no calls placed yields zero rates, not NaN",
			campaign:        OutboundCampaign{},
			wantPickupRate:  0,
			wantSuccessRate: 0,
		},
		{
			name: "documented dashboard figures",
			campaign: OutboundCampaign{
				CallsPlaced:     656,
				AnsweredCalls:   412,
				SuccessfulCalls: 188,
			},
			wantPickupRate:  0.628,
			wantSuccessRate: 0.287,
		},
		{
			name: "every call answered and successful",
			campaign: OutboundCampaign{
				CallsPlaced:     10,
				AnsweredCalls:   10,
				SuccessfulCalls: 10,
			},
			wantPickupRate:  1,
			wantSuccessRate: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			campaign := tt.campaign
			campaign.ApplyDerivedFields(today)

			if campaign.PickupRate != tt.wantPickupRate {
				t.Errorf("pickup_rate = %v, want %v", campaign.PickupRate, tt.wantPickupRate)
			}
			if campaign.SuccessRate != tt.wantSuccessRate {
				t.Errorf("success_rate = %v, want %v", campaign.SuccessRate, tt.wantSuccessRate)
			}
		})
	}
}

// TestApplyDerivedFieldsTodayCalls pins the staleness rule: a counter is only
// reported when the day it counts is today.
func TestApplyDerivedFieldsTodayCalls(t *testing.T) {
	today := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	yesterday := "2026-08-09"
	todayStr := "2026-08-10"

	tests := []struct {
		name string
		date *string
		want int
	}{
		{name: "counted today", date: &todayStr, want: 37},
		{name: "left over from yesterday", date: &yesterday, want: 0},
		{name: "never dialled", date: nil, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			campaign := OutboundCampaign{TodayCalls: 37, TodayCallsDate: tt.date}
			campaign.ApplyDerivedFields(today)

			if campaign.TodayCalls != tt.want {
				t.Errorf("today_calls = %d, want %d", campaign.TodayCalls, tt.want)
			}
		})
	}
}

func TestOutboundCampaignUpdateIsEmpty(t *testing.T) {
	name := "July reactivation"
	budget := 250.0

	tests := []struct {
		name   string
		update OutboundCampaignUpdate
		want   bool
	}{
		{name: "nothing supplied", update: OutboundCampaignUpdate{}, want: true},
		{name: "name supplied", update: OutboundCampaignUpdate{Name: &name}, want: false},
		{name: "budget supplied", update: OutboundCampaignUpdate{BudgetUSD: &budget}, want: false},
		// Clearing the budget writes NULL, so it is a change even though every
		// pointer is nil.
		{name: "budget cleared", update: OutboundCampaignUpdate{ClearBudget: true}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.update.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}
