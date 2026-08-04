package domain

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestParseCanonicalIDsMatchesFrozenPrefixValidation(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		parse   func(string) error
		wantErr bool
	}{
		{name: "event", value: "evt_test", parse: func(value string) error { _, err := ParseEventID(value); return err }},
		{name: "event missing underscore", value: "evttest", parse: func(value string) error { _, err := ParseEventID(value); return err }, wantErr: true},
		{name: "message", value: "msg_test", parse: func(value string) error { _, err := ParseMessageID(value); return err }},
		{name: "message missing underscore", value: "msgtest", parse: func(value string) error { _, err := ParseMessageID(value); return err }, wantErr: true},
		{name: "session canonical", value: "ses_test", parse: func(value string) error { _, err := ParseSessionID(value); return err }},
		{name: "session frozen loose prefix", value: "session", parse: func(value string) error { _, err := ParseSessionID(value); return err }},
		{name: "session invalid", value: "se_test", parse: func(value string) error { _, err := ParseSessionID(value); return err }, wantErr: true},
		{name: "permission frozen loose prefix", value: "permission", parse: func(value string) error { _, err := ParsePermissionID(value); return err }},
		{name: "permission invalid", value: "pe_test", parse: func(value string) error { _, err := ParsePermissionID(value); return err }, wantErr: true},
		{name: "question frozen loose prefix", value: "question", parse: func(value string) error { _, err := ParseQuestionID(value); return err }},
		{name: "workspace frozen loose prefix", value: "wrkspace", parse: func(value string) error { _, err := ParseWorkspaceID(value); return err }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.parse(test.value)
			if test.wantErr && err == nil {
				t.Fatal("parse unexpectedly succeeded")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("parse: %v", err)
			}
		})
	}
}

func TestIdentifierGeneratorMatchesFrozenAscendingAndDescendingLayout(t *testing.T) {
	generator := NewIdentifierGenerator(
		func() time.Time { return time.UnixMilli(1) },
		bytes.NewReader(make([]byte, 56)),
	)

	eventID, err := generator.NewEventID()
	if err != nil {
		t.Fatalf("new event ID: %v", err)
	}
	if eventID != EventID("evt_00000000100100000000000000") {
		t.Fatalf("event ID = %q", eventID)
	}
	messageID, err := generator.NewMessageID()
	if err != nil {
		t.Fatalf("new message ID: %v", err)
	}
	if messageID != MessageID("msg_00000000100200000000000000") {
		t.Fatalf("message ID = %q", messageID)
	}
	sessionID, err := generator.NewSessionID()
	if err != nil {
		t.Fatalf("new session ID: %v", err)
	}
	if sessionID != SessionID("ses_ffffffffeffc00000000000000") {
		t.Fatalf("session ID = %q", sessionID)
	}
}

func TestCanonicalIDJSONRoundTripUsesStrings(t *testing.T) {
	input := struct {
		Event   EventID   `json:"event"`
		Session SessionID `json:"session"`
	}{Event: EventID("evt_test"), Session: SessionID("ses_test")}

	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode IDs: %v", err)
	}
	if string(encoded) != `{"event":"evt_test","session":"ses_test"}` {
		t.Fatalf("encoded = %s", encoded)
	}
	var decoded struct {
		Event   EventID   `json:"event"`
		Session SessionID `json:"session"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode IDs: %v", err)
	}
	if decoded != input {
		t.Fatalf("decoded = %#v, want %#v", decoded, input)
	}
}
