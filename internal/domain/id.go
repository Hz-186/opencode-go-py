package domain

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	identifierLength = 26
	identifierTime   = 12
	identifierMask   = uint64(1<<48) - 1
)

const identifierCharacters = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

type EventID string
type MessageID string
type SessionID string
type PermissionID string
type QuestionID string
type WorkspaceID string
type ProjectID string

func ParseEventID(value string) (EventID, error) {
	return parsePrefixedID[EventID](value, "evt_", "event ID")
}

func ParseMessageID(value string) (MessageID, error) {
	return parsePrefixedID[MessageID](value, "msg_", "message ID")
}

func ParseSessionID(value string) (SessionID, error) {
	return parsePrefixedID[SessionID](value, "ses", "session ID")
}

func ParsePermissionID(value string) (PermissionID, error) {
	return parsePrefixedID[PermissionID](value, "per", "permission ID")
}

func ParseQuestionID(value string) (QuestionID, error) {
	return parsePrefixedID[QuestionID](value, "que", "question ID")
}

func ParseWorkspaceID(value string) (WorkspaceID, error) {
	return parsePrefixedID[WorkspaceID](value, "wrk", "workspace ID")
}

func parsePrefixedID[ID ~string](value string, prefix string, label string) (ID, error) {
	if !strings.HasPrefix(value, prefix) {
		return "", fmt.Errorf("%s %q does not start with %q", label, value, prefix)
	}
	return ID(value), nil
}

func (id *EventID) UnmarshalJSON(content []byte) error {
	return unmarshalID(content, ParseEventID, id)
}

func (id *MessageID) UnmarshalJSON(content []byte) error {
	return unmarshalID(content, ParseMessageID, id)
}

func (id *SessionID) UnmarshalJSON(content []byte) error {
	return unmarshalID(content, ParseSessionID, id)
}

func (id *PermissionID) UnmarshalJSON(content []byte) error {
	return unmarshalID(content, ParsePermissionID, id)
}

func (id *QuestionID) UnmarshalJSON(content []byte) error {
	return unmarshalID(content, ParseQuestionID, id)
}

func (id *WorkspaceID) UnmarshalJSON(content []byte) error {
	return unmarshalID(content, ParseWorkspaceID, id)
}

func unmarshalID[ID ~string](content []byte, parse func(string) (ID, error), target *ID) error {
	if target == nil {
		return errors.New("ID target is nil")
	}
	var value string
	if err := json.Unmarshal(content, &value); err != nil {
		return err
	}
	parsed, err := parse(value)
	if err != nil {
		return err
	}
	*target = parsed
	return nil
}

type IdentifierGenerator struct {
	now           func() time.Time
	random        io.Reader
	mu            sync.Mutex
	lastTimestamp int64
	counter       uint64
}

func NewIdentifierGenerator(now func() time.Time, random io.Reader) *IdentifierGenerator {
	return &IdentifierGenerator{now: now, random: random}
}

func (generator *IdentifierGenerator) NewEventID() (EventID, error) {
	value, err := generator.create("evt_", false)
	return EventID(value), err
}

func (generator *IdentifierGenerator) NewMessageID() (MessageID, error) {
	value, err := generator.create("msg_", false)
	return MessageID(value), err
}

func (generator *IdentifierGenerator) NewSessionID() (SessionID, error) {
	value, err := generator.create("ses_", true)
	return SessionID(value), err
}

func (generator *IdentifierGenerator) NewPermissionID() (PermissionID, error) {
	value, err := generator.create("per_", false)
	return PermissionID(value), err
}

func (generator *IdentifierGenerator) NewQuestionID() (QuestionID, error) {
	value, err := generator.create("que_", false)
	return QuestionID(value), err
}

func (generator *IdentifierGenerator) NewWorkspaceID() (WorkspaceID, error) {
	value, err := generator.create("wrk_", false)
	return WorkspaceID(value), err
}

func (generator *IdentifierGenerator) create(prefix string, descending bool) (string, error) {
	if generator == nil || generator.now == nil || generator.random == nil {
		return "", errors.New("identifier generator is incomplete")
	}
	generator.mu.Lock()
	defer generator.mu.Unlock()

	timestamp := generator.now().UnixMilli()
	if timestamp < 0 {
		return "", fmt.Errorf("identifier timestamp %d is before Unix epoch", timestamp)
	}
	if timestamp != generator.lastTimestamp {
		generator.lastTimestamp = timestamp
		generator.counter = 0
	}
	generator.counter++
	current := (uint64(timestamp)*0x1000 + generator.counter) & identifierMask
	if descending {
		current = (^current) & identifierMask
	}
	var timeBytes [8]byte
	binary.BigEndian.PutUint64(timeBytes[:], current)
	timePart := hex.EncodeToString(timeBytes[2:])

	randomBytes := make([]byte, identifierLength-identifierTime)
	if _, err := io.ReadFull(generator.random, randomBytes); err != nil {
		return "", fmt.Errorf("read identifier randomness: %w", err)
	}
	randomPart := make([]byte, len(randomBytes))
	for index, value := range randomBytes {
		randomPart[index] = identifierCharacters[int(value)%len(identifierCharacters)]
	}
	return prefix + timePart + string(randomPart), nil
}
