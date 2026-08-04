package codec

import (
	"errors"
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

type sessionEventContract struct {
	durable bool
	version int64
	data    contractRule
}

var sessionEventContracts = buildSessionEventContracts()

func DecodeSessionEventJSON(content []byte) (domain.SessionEvent, error) {
	envelope, err := DecodeEventEnvelopeJSON(content)
	if err != nil {
		return nil, err
	}
	contract, known := sessionEventContracts[envelope.Type]
	if !known {
		return domain.UnknownSessionEvent{Value: envelope}, nil
	}
	if err := contract.data(envelope.Data); err != nil {
		return nil, fmt.Errorf("decode known session event %q: %w", envelope.Type, err)
	}
	return domain.KnownSessionEvent{
		Value: envelope, DurableDefinition: contract.durable, DefinitionVersion: contract.version,
	}, nil
}

func EncodeSessionEventJSON(event domain.SessionEvent) ([]byte, error) {
	if event == nil {
		return nil, errors.New("session event is nil")
	}
	var envelope domain.EventEnvelope
	switch event := event.(type) {
	case domain.KnownSessionEvent:
		contract, known := sessionEventContracts[event.Value.Type]
		if !known {
			return nil, fmt.Errorf("unknown type %q cannot use KnownSessionEvent", event.Value.Type)
		}
		if event.DurableDefinition != contract.durable || event.DefinitionVersion != contract.version {
			return nil, fmt.Errorf("session event definition metadata does not match %q", event.Value.Type)
		}
		if err := contract.data(event.Value.Data); err != nil {
			return nil, fmt.Errorf("validate known session event %q: %w", event.Value.Type, err)
		}
		envelope = event.Value
	case domain.UnknownSessionEvent:
		if _, known := sessionEventContracts[event.Value.Type]; known {
			return nil, fmt.Errorf("known session event type %q must use KnownSessionEvent", event.Value.Type)
		}
		envelope = event.Value
	default:
		return nil, fmt.Errorf("unsupported session event value %T", event)
	}
	return EncodeEventEnvelopeJSON(envelope)
}

func buildSessionEventContracts() map[string]sessionEventContract {
	modelRef := contractObjectShape("model ref", map[string]contractFieldRule{
		"id": requiredShape(contractStringShape), "providerID": requiredShape(contractStringShape),
		"variant": optionalShape(contractStringShape),
	})
	promptSource := contractObjectShape("prompt source", map[string]contractFieldRule{
		"start": requiredShape(contractFiniteNumberShape), "end": requiredShape(contractFiniteNumberShape),
		"text": requiredShape(contractStringShape),
	})
	fileAttachment := contractObjectShape("file attachment", map[string]contractFieldRule{
		"uri": requiredShape(contractStringShape), "mime": requiredShape(contractStringShape),
		"name": optionalShape(contractStringShape), "description": optionalShape(contractStringShape),
		"source": optionalShape(promptSource),
	})
	agentAttachment := contractObjectShape("agent attachment", map[string]contractFieldRule{
		"name": requiredShape(contractStringShape), "source": optionalShape(promptSource),
	})
	prompt := contractObjectShape("prompt", map[string]contractFieldRule{
		"text":   requiredShape(contractStringShape),
		"files":  optionalShape(contractArrayShape("prompt files", fileAttachment)),
		"agents": optionalShape(contractArrayShape("prompt agents", agentAttachment)),
	})
	location := contractObjectShape("location ref", map[string]contractFieldRule{
		"directory": requiredShape(contractStringShape), "workspaceID": optionalShape(contractWorkspaceIDShape),
	})
	unknownError := contractObjectShape("unknown error", map[string]contractFieldRule{
		"type": requiredShape(contractLiteralShape("unknown")), "message": requiredShape(contractStringShape),
	})
	tokens := contractObjectShape("tokens", map[string]contractFieldRule{
		"input": requiredShape(contractFiniteNumberShape), "output": requiredShape(contractFiniteNumberShape),
		"reasoning": requiredShape(contractFiniteNumberShape),
		"cache": requiredShape(contractObjectShape("token cache", map[string]contractFieldRule{
			"read": requiredShape(contractFiniteNumberShape), "write": requiredShape(contractFiniteNumberShape),
		})),
	})
	toolContent := contractToolContentShape()
	toolProvider := contractObjectShape("tool provider", map[string]contractFieldRule{
		"executed": requiredShape(contractBoolShape), "metadata": optionalShape(contractProviderMetadataShape),
	})
	retryError := contractObjectShape("retry error", map[string]contractFieldRule{
		"message": requiredShape(contractStringShape), "statusCode": optionalShape(contractFiniteNumberShape),
		"isRetryable": requiredShape(contractBoolShape), "responseHeaders": optionalShape(contractStringRecordShape),
		"responseBody": optionalShape(contractStringShape), "metadata": optionalShape(contractStringRecordShape),
	})
	revertState := contractRevertStateShape()

	contracts := make(map[string]sessionEventContract, 32)
	add := func(typeName string, durable bool, version int64, fields map[string]contractFieldRule) {
		base := map[string]contractFieldRule{
			"timestamp": requiredShape(contractFiniteNumberShape), "sessionID": requiredShape(contractSessionIDShape),
		}
		for field, rule := range fields {
			base[field] = rule
		}
		contracts[typeName] = sessionEventContract{
			durable: durable, version: version, data: contractObjectShape(typeName+" data", base),
		}
	}

	add("session.next.agent.switched", true, 1, map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "agent": requiredShape(contractStringShape),
	})
	add("session.next.model.switched", true, 1, map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "model": requiredShape(modelRef),
	})
	add("session.next.moved", true, 1, map[string]contractFieldRule{
		"location": requiredShape(location), "subdirectory": optionalShape(contractStringShape),
	})
	promptFields := map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "prompt": requiredShape(prompt),
		"delivery": requiredShape(contractLiteralShape("steer", "queue")),
	}
	add("session.next.prompted", true, 1, promptFields)
	add("session.next.prompt.admitted", true, 1, promptFields)
	add("session.next.context.updated", true, 1, messageTextFields("text"))
	add("session.next.synthetic", true, 1, messageTextFields("text"))
	add("session.next.shell.started", true, 1, map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "callID": requiredShape(contractStringShape),
		"command": requiredShape(contractStringShape),
	})
	add("session.next.shell.ended", true, 1, map[string]contractFieldRule{
		"callID": requiredShape(contractStringShape), "output": requiredShape(contractStringShape),
	})
	add("session.next.step.started", true, 1, map[string]contractFieldRule{
		"assistantMessageID": requiredShape(contractMessageIDShape), "agent": requiredShape(contractStringShape),
		"model": requiredShape(modelRef), "snapshot": optionalShape(contractStringShape),
	})
	add("session.next.step.ended", true, 2, map[string]contractFieldRule{
		"assistantMessageID": requiredShape(contractMessageIDShape), "finish": requiredShape(contractStringShape),
		"cost": requiredShape(contractFiniteNumberShape), "tokens": requiredShape(tokens),
		"snapshot": optionalShape(contractStringShape), "files": optionalShape(contractArrayShape("step files", contractStringShape)),
	})
	add("session.next.step.failed", true, 2, map[string]contractFieldRule{
		"assistantMessageID": requiredShape(contractMessageIDShape), "error": requiredShape(unknownError),
	})
	add("session.next.text.started", true, 1, assistantBlockFields("textID", nil))
	add("session.next.text.delta", false, 0, assistantBlockFields("textID", map[string]contractFieldRule{"delta": requiredShape(contractStringShape)}))
	add("session.next.text.ended", true, 1, assistantBlockFields("textID", map[string]contractFieldRule{"text": requiredShape(contractStringShape)}))
	add("session.next.reasoning.started", true, 1, assistantBlockFields("reasoningID", map[string]contractFieldRule{"providerMetadata": optionalShape(contractProviderMetadataShape)}))
	add("session.next.reasoning.delta", false, 0, assistantBlockFields("reasoningID", map[string]contractFieldRule{"delta": requiredShape(contractStringShape)}))
	add("session.next.reasoning.ended", true, 1, assistantBlockFields("reasoningID", map[string]contractFieldRule{
		"text": requiredShape(contractStringShape), "providerMetadata": optionalShape(contractProviderMetadataShape),
	}))
	add("session.next.tool.input.started", true, 1, toolBaseFields(map[string]contractFieldRule{"name": requiredShape(contractStringShape)}))
	add("session.next.tool.input.delta", false, 0, toolBaseFields(map[string]contractFieldRule{"delta": requiredShape(contractStringShape)}))
	add("session.next.tool.input.ended", true, 1, toolBaseFields(map[string]contractFieldRule{"text": requiredShape(contractStringShape)}))
	add("session.next.tool.called", true, 1, toolBaseFields(map[string]contractFieldRule{
		"tool": requiredShape(contractStringShape), "input": requiredShape(contractRecordShape),
		"provider": requiredShape(toolProvider),
	}))
	add("session.next.tool.progress", true, 1, toolBaseFields(map[string]contractFieldRule{
		"structured": requiredShape(contractRecordShape), "content": requiredShape(contractArrayShape("tool content", toolContent)),
	}))
	add("session.next.tool.success", true, 1, toolBaseFields(map[string]contractFieldRule{
		"structured": requiredShape(contractRecordShape), "content": requiredShape(contractArrayShape("tool content", toolContent)),
		"outputPaths": optionalShape(contractArrayShape("output paths", contractStringShape)),
		"result":      optionalShape(contractNonNullUnknownShape), "provider": requiredShape(toolProvider),
	}))
	add("session.next.tool.failed", true, 1, toolBaseFields(map[string]contractFieldRule{
		"error": requiredShape(unknownError), "result": optionalShape(contractNonNullUnknownShape),
		"provider": requiredShape(toolProvider),
	}))
	add("session.next.retried", true, 1, map[string]contractFieldRule{
		"attempt": requiredShape(contractFiniteNumberShape), "error": requiredShape(retryError),
	})
	add("session.next.compaction.started", true, 1, map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "reason": requiredShape(contractLiteralShape("auto", "manual")),
	})
	add("session.next.compaction.delta", false, 0, messageTextFields("text"))
	add("session.next.compaction.ended", true, 1, map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "reason": requiredShape(contractLiteralShape("auto", "manual")),
		"text": requiredShape(contractStringShape), "recent": requiredShape(contractStringShape),
	})
	add("session.next.revert.staged", true, 1, map[string]contractFieldRule{"revert": requiredShape(revertState)})
	add("session.next.revert.cleared", true, 1, nil)
	add("session.next.revert.committed", true, 1, map[string]contractFieldRule{"messageID": requiredShape(contractMessageIDShape)})
	return contracts
}

func messageTextFields(textField string) map[string]contractFieldRule {
	return map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), textField: requiredShape(contractStringShape),
	}
}

func assistantBlockFields(idField string, extra map[string]contractFieldRule) map[string]contractFieldRule {
	fields := map[string]contractFieldRule{
		"assistantMessageID": requiredShape(contractMessageIDShape), idField: requiredShape(contractStringShape),
	}
	for field, rule := range extra {
		fields[field] = rule
	}
	return fields
}

func toolBaseFields(extra map[string]contractFieldRule) map[string]contractFieldRule {
	fields := map[string]contractFieldRule{
		"assistantMessageID": requiredShape(contractMessageIDShape), "callID": requiredShape(contractStringShape),
	}
	for field, rule := range extra {
		fields[field] = rule
	}
	return fields
}

func contractToolContentShape() contractRule {
	text := contractObjectShape("tool text content", map[string]contractFieldRule{
		"type": requiredShape(contractLiteralShape("text")), "text": requiredShape(contractStringShape),
	})
	file := contractObjectShape("tool file content", map[string]contractFieldRule{
		"type": requiredShape(contractLiteralShape("file")), "uri": requiredShape(contractStringShape),
		"mime": requiredShape(contractStringShape), "name": optionalShape(contractStringShape),
	})
	return func(value domain.JSONValue) error {
		if value.Kind != domain.JSONKindObject {
			return errors.New("tool content must be an object")
		}
		typeValue, present := value.Object["type"]
		if !present || typeValue.Kind != domain.JSONKindString {
			return errors.New("tool content type must be a string")
		}
		switch typeValue.String {
		case "text":
			return text(value)
		case "file":
			return file(value)
		default:
			return fmt.Errorf("unknown tool content type %q", typeValue.String)
		}
	}
}

func contractRevertStateShape() contractRule {
	fileDiff := contractObjectShape("revert file diff", map[string]contractFieldRule{
		"path": requiredShape(contractStringShape), "status": requiredShape(contractLiteralShape("added", "modified", "deleted")),
		"additions": requiredShape(contractNonNegativeIntShape), "deletions": requiredShape(contractNonNegativeIntShape),
		"patch": requiredShape(contractStringShape),
	})
	return contractObjectShape("revert state", map[string]contractFieldRule{
		"messageID": requiredShape(contractMessageIDShape), "partID": optionalShape(contractStringShape),
		"snapshot": optionalShape(contractStringShape), "diff": optionalShape(contractStringShape),
		"files": optionalShape(contractArrayShape("revert files", fileDiff)),
	})
}
