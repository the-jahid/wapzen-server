package chatagents

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestOptionalStringDistinguishesOmittedAndNull(t *testing.T) {
	var req UpdateRequest
	if err := json.Unmarshal([]byte(`{"agent":{"phone_number_id":null}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Agent == nil || !req.Agent.PhoneNumberID.Set || req.Agent.PhoneNumberID.Value != nil {
		t.Fatalf("explicit null was not preserved: %#v", req.Agent)
	}
}

func TestValidateChatAgentSettings(t *testing.T) {
	temperature := 1.1
	err := ValidateCreate(CreateRequest{
		Agent: &CreateAgentSection{Name: "Support", Status: "sideways"},
		Model: &CreateModelSection{Temperature: &temperature},
	})
	if err == nil {
		t.Fatal("invalid status and temperature were accepted")
	}

	if err := ValidateCreate(CreateRequest{Agent: &CreateAgentSection{Name: "Support"}}); err != nil {
		t.Fatalf("minimal valid create was rejected: %v", err)
	}
	if err := ValidateUpdate(UpdateRequest{}); err == nil {
		t.Fatal("empty update was accepted")
	}
	if err := ValidateUpdate(UpdateRequest{Agent: &UpdateAgentSection{}}); err == nil {
		t.Fatal("empty agent section was accepted")
	}
}

func TestActiveChatAgentRequiresPhoneNumber(t *testing.T) {
	if err := ValidateCreate(CreateRequest{
		Agent: &CreateAgentSection{Name: "Support", Status: "active"},
	}); !errors.Is(err, ErrPhoneNumberRequired) {
		t.Fatalf("active create error = %v, want ErrPhoneNumberRequired", err)
	}

	phoneNumberID := "phone_123"
	if err := ValidateCreate(CreateRequest{
		Agent: &CreateAgentSection{Name: "Support", Status: "active", PhoneNumberID: &phoneNumberID},
	}); err != nil {
		t.Fatalf("active create with a phone number was rejected: %v", err)
	}

	if err := validateActiveHasPhone("active", nil); !errors.Is(err, ErrPhoneNumberRequired) {
		t.Fatalf("active update state error = %v, want ErrPhoneNumberRequired", err)
	}
	if err := validateActiveHasPhone("inactive", nil); err != nil {
		t.Fatalf("inactive agent without a phone number was rejected: %v", err)
	}
}
