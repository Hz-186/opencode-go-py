package codec

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func DecodeEventEnvelopeJSON(content []byte) (domain.EventEnvelope, error) {
	object, err := decodeContractObject(content, "event envelope")
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	if err := rejectUnknownContractFields(object, "event envelope", "id", "type", "data", "durable", "location", "metadata"); err != nil {
		return domain.EventEnvelope{}, err
	}
	idValue, err := requiredContractString(object, "id", "event envelope")
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	id, err := domain.ParseEventID(idValue)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	typeName, err := requiredContractString(object, "type", "event envelope")
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	if typeName == "" {
		return domain.EventEnvelope{}, errors.New("event envelope type must not be empty")
	}
	data, err := requireContractValue(object, "data", "event envelope")
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	if data.Kind != domain.JSONKindObject {
		return domain.EventEnvelope{}, errors.New("event envelope data must be an object")
	}
	durable, err := decodeEventDurable(object)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	location, err := decodeEventLocation(object)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "event envelope")
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	return domain.EventEnvelope{
		ID: id, Type: typeName, Data: data, Durable: durable, Location: location, Metadata: metadata,
	}, nil
}

func EncodeEventEnvelopeJSON(event domain.EventEnvelope) ([]byte, error) {
	if _, err := domain.ParseEventID(string(event.ID)); err != nil {
		return nil, err
	}
	if event.Type == "" {
		return nil, errors.New("event envelope type must not be empty")
	}
	if event.Data.Kind != domain.JSONKindObject {
		return nil, errors.New("event envelope data must be an object")
	}
	object := map[string]domain.JSONValue{
		"id": domain.JSONString(string(event.ID)), "type": domain.JSONString(event.Type), "data": event.Data,
	}
	if event.Durable != nil {
		object["durable"] = domain.JSONObject(map[string]domain.JSONValue{
			"aggregateID": domain.JSONString(event.Durable.AggregateID),
			"seq":         domain.JSONNumber(strconv.FormatInt(event.Durable.Sequence, 10)),
			"version":     domain.JSONNumber(strconv.FormatInt(event.Durable.Version, 10)),
		})
	}
	if event.Location != nil {
		if event.Location.WorkspaceID != nil {
			if _, err := domain.ParseWorkspaceID(string(*event.Location.WorkspaceID)); err != nil {
				return nil, err
			}
		}
		location := map[string]domain.JSONValue{"directory": domain.JSONString(event.Location.Directory)}
		if event.Location.WorkspaceID != nil {
			location["workspaceID"] = domain.JSONString(string(*event.Location.WorkspaceID))
		}
		object["location"] = domain.JSONObject(location)
	}
	if event.Metadata != nil {
		object["metadata"] = domain.JSONObject(event.Metadata)
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeEventEnvelopeJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded event envelope: %w", err)
	}
	return encoded, nil
}

func decodeEventDurable(object map[string]domain.JSONValue) (*domain.EventDurable, error) {
	durableObject, present, err := optionalContractObject(object, "durable", "event envelope")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(durableObject, "event durable", "aggregateID", "seq", "version"); err != nil {
		return nil, err
	}
	aggregateID, err := requiredContractString(durableObject, "aggregateID", "event durable")
	if err != nil {
		return nil, err
	}
	sequence, err := requiredContractInt64(durableObject, "seq", "event durable")
	if err != nil {
		return nil, err
	}
	version, err := requiredContractInt64(durableObject, "version", "event durable")
	if err != nil {
		return nil, err
	}
	return &domain.EventDurable{AggregateID: aggregateID, Sequence: sequence, Version: version}, nil
}

func decodeEventLocation(object map[string]domain.JSONValue) (*domain.LocationRef, error) {
	locationObject, present, err := optionalContractObject(object, "location", "event envelope")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(locationObject, "event location", "directory", "workspaceID"); err != nil {
		return nil, err
	}
	directory, err := requiredContractString(locationObject, "directory", "event location")
	if err != nil {
		return nil, err
	}
	location := &domain.LocationRef{Directory: directory}
	if workspaceValue, present := locationObject["workspaceID"]; present {
		if workspaceValue.Kind != domain.JSONKindString {
			return nil, errors.New("event location workspaceID must be a string when present")
		}
		workspaceID, err := domain.ParseWorkspaceID(workspaceValue.String)
		if err != nil {
			return nil, err
		}
		location.WorkspaceID = &workspaceID
	}
	return location, nil
}

func requiredContractInt64(object map[string]domain.JSONValue, field string, label string) (int64, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindNumber {
		return 0, fmt.Errorf("%s %s must be an integer", label, field)
	}
	number, _, err := big.ParseFloat(value.Number, 10, 256, big.ToNearestEven)
	if err != nil {
		return 0, fmt.Errorf("%s %s must be an integer: %w", label, field, err)
	}
	integer, accuracy := number.Int(nil)
	if accuracy != big.Exact || !integer.IsInt64() {
		return 0, fmt.Errorf("%s %s must be a 64-bit integer", label, field)
	}
	return integer.Int64(), nil
}
