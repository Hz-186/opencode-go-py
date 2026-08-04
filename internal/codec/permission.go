package codec

import (
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func DecodePermissionRequestJSON(content []byte) (domain.PermissionRequest, error) {
	object, err := decodeContractObject(content, "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	if err := rejectUnknownContractFields(object, "permission request", "id", "sessionID", "action", "resources", "save", "metadata", "source"); err != nil {
		return domain.PermissionRequest{}, err
	}
	idValue, err := requiredContractString(object, "id", "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	id, err := domain.ParsePermissionID(idValue)
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	sessionValue, err := requiredContractString(object, "sessionID", "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	sessionID, err := domain.ParseSessionID(sessionValue)
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	action, err := requiredContractString(object, "action", "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	resourcesValue, err := requireContractValue(object, "resources", "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	resources, err := decodeContractStringArray(resourcesValue, "permission request resources")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	save, err := optionalContractStringArray(object, "save", "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "permission request")
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	source, err := decodePermissionSource(object)
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	return domain.PermissionRequest{
		ID: id, SessionID: sessionID, Action: action, Resources: resources,
		Save: save, Metadata: metadata, Source: source,
	}, nil
}

func EncodePermissionRequestJSON(request domain.PermissionRequest) ([]byte, error) {
	if _, err := domain.ParsePermissionID(string(request.ID)); err != nil {
		return nil, err
	}
	if _, err := domain.ParseSessionID(string(request.SessionID)); err != nil {
		return nil, err
	}
	object := map[string]domain.JSONValue{
		"id":        domain.JSONString(string(request.ID)),
		"sessionID": domain.JSONString(string(request.SessionID)),
		"action":    domain.JSONString(request.Action),
		"resources": contractStringArray(request.Resources),
	}
	if request.Save != nil {
		object["save"] = contractStringArray(request.Save)
	}
	if request.Metadata != nil {
		object["metadata"] = domain.JSONObject(request.Metadata)
	}
	if request.Source != nil {
		if request.Source.Type != domain.PermissionSourceTool {
			return nil, fmt.Errorf("invalid permission source type %q", request.Source.Type)
		}
		object["source"] = domain.JSONObject(map[string]domain.JSONValue{
			"type":      domain.JSONString(string(request.Source.Type)),
			"messageID": domain.JSONString(request.Source.MessageID),
			"callID":    domain.JSONString(request.Source.CallID),
		})
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, err
	}
	if _, err := DecodePermissionRequestJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded permission request: %w", err)
	}
	return encoded, nil
}

func decodePermissionSource(object map[string]domain.JSONValue) (*domain.PermissionSource, error) {
	sourceObject, present, err := optionalContractObject(object, "source", "permission request")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(sourceObject, "permission source", "type", "messageID", "callID"); err != nil {
		return nil, err
	}
	typeValue, err := requiredContractString(sourceObject, "type", "permission source")
	if err != nil {
		return nil, err
	}
	if err := validateContractEnum(typeValue, "permission source type", string(domain.PermissionSourceTool)); err != nil {
		return nil, err
	}
	messageID, err := requiredContractString(sourceObject, "messageID", "permission source")
	if err != nil {
		return nil, err
	}
	callID, err := requiredContractString(sourceObject, "callID", "permission source")
	if err != nil {
		return nil, err
	}
	return &domain.PermissionSource{Type: domain.PermissionSourceType(typeValue), MessageID: messageID, CallID: callID}, nil
}

func DecodePermissionReplyJSON(content []byte) (domain.PermissionReply, error) {
	value, err := DecodeJSONValue(content)
	if err != nil {
		return "", err
	}
	if value.Kind != domain.JSONKindString {
		return "", fmt.Errorf("permission reply must be a string")
	}
	if err := validatePermissionReply(domain.PermissionReply(value.String)); err != nil {
		return "", err
	}
	return domain.PermissionReply(value.String), nil
}

func EncodePermissionReplyJSON(reply domain.PermissionReply) ([]byte, error) {
	if err := validatePermissionReply(reply); err != nil {
		return nil, err
	}
	return EncodeJSONValue(domain.JSONString(string(reply)))
}

func validatePermissionReply(reply domain.PermissionReply) error {
	return validateContractEnum(string(reply), "permission reply", string(domain.PermissionReplyOnce), string(domain.PermissionReplyAlways), string(domain.PermissionReplyReject))
}

func DecodePermissionRulesetJSON(content []byte) (domain.PermissionRuleset, error) {
	value, err := DecodeJSONValue(content)
	if err != nil {
		return nil, err
	}
	if value.Kind != domain.JSONKindArray {
		return nil, fmt.Errorf("permission ruleset must be an array")
	}
	rules := make(domain.PermissionRuleset, len(value.Array))
	for index, item := range value.Array {
		if item.Kind != domain.JSONKindObject {
			return nil, fmt.Errorf("permission rule %d must be an object", index)
		}
		if err := rejectUnknownContractFields(item.Object, "permission rule", "action", "resource", "effect"); err != nil {
			return nil, err
		}
		action, err := requiredContractString(item.Object, "action", "permission rule")
		if err != nil {
			return nil, err
		}
		resource, err := requiredContractString(item.Object, "resource", "permission rule")
		if err != nil {
			return nil, err
		}
		effectValue, err := requiredContractString(item.Object, "effect", "permission rule")
		if err != nil {
			return nil, err
		}
		effect := domain.PermissionEffect(effectValue)
		if err := validatePermissionEffect(effect); err != nil {
			return nil, err
		}
		rules[index] = domain.PermissionRule{Action: action, Resource: resource, Effect: effect}
	}
	return rules, nil
}

func EncodePermissionRulesetJSON(rules domain.PermissionRuleset) ([]byte, error) {
	items := make([]domain.JSONValue, len(rules))
	for index, rule := range rules {
		if err := validatePermissionEffect(rule.Effect); err != nil {
			return nil, err
		}
		items[index] = domain.JSONObject(map[string]domain.JSONValue{
			"action": domain.JSONString(rule.Action), "resource": domain.JSONString(rule.Resource),
			"effect": domain.JSONString(string(rule.Effect)),
		})
	}
	return EncodeJSONValue(domain.JSONArray(items))
}

func validatePermissionEffect(effect domain.PermissionEffect) error {
	return validateContractEnum(string(effect), "permission effect", string(domain.PermissionEffectAllow), string(domain.PermissionEffectDeny), string(domain.PermissionEffectAsk))
}
