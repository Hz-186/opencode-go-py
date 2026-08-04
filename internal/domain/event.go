package domain

type EventDurable struct {
	AggregateID string
	Sequence    int64
	Version     int64
}

type EventEnvelope struct {
	ID       EventID
	Type     string
	Data     JSONValue
	Durable  *EventDurable
	Location *LocationRef
	Metadata map[string]JSONValue
}

type SessionEvent interface {
	Type() string
	Envelope() EventEnvelope
}

type KnownSessionEvent struct {
	Value             EventEnvelope
	DurableDefinition bool
	DefinitionVersion int64
}

func (event KnownSessionEvent) Type() string            { return event.Value.Type }
func (event KnownSessionEvent) Envelope() EventEnvelope { return event.Value }

type UnknownSessionEvent struct {
	Value EventEnvelope
}

func (event UnknownSessionEvent) Type() string            { return event.Value.Type }
func (event UnknownSessionEvent) Envelope() EventEnvelope { return event.Value }
