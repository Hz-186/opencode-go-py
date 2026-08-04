package codec

import (
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func DecodeQuestionRequestJSON(content []byte) (domain.QuestionRequest, error) {
	object, err := decodeContractObject(content, "question request")
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	if err := rejectUnknownContractFields(object, "question request", "id", "sessionID", "questions", "tool"); err != nil {
		return domain.QuestionRequest{}, err
	}
	idValue, err := requiredContractString(object, "id", "question request")
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	id, err := domain.ParseQuestionID(idValue)
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	sessionValue, err := requiredContractString(object, "sessionID", "question request")
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	sessionID, err := domain.ParseSessionID(sessionValue)
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	questionValues, err := requiredContractArray(object, "questions", "question request")
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	questions := make([]domain.QuestionInfo, len(questionValues))
	for index, value := range questionValues {
		questions[index], err = decodeQuestionInfo(value, index)
		if err != nil {
			return domain.QuestionRequest{}, err
		}
	}
	tool, err := decodeQuestionTool(object)
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	return domain.QuestionRequest{ID: id, SessionID: sessionID, Questions: questions, Tool: tool}, nil
}

func EncodeQuestionRequestJSON(request domain.QuestionRequest) ([]byte, error) {
	if _, err := domain.ParseQuestionID(string(request.ID)); err != nil {
		return nil, err
	}
	if _, err := domain.ParseSessionID(string(request.SessionID)); err != nil {
		return nil, err
	}
	questions := make([]domain.JSONValue, len(request.Questions))
	for index, question := range request.Questions {
		options := make([]domain.JSONValue, len(question.Options))
		for optionIndex, option := range question.Options {
			options[optionIndex] = domain.JSONObject(map[string]domain.JSONValue{
				"label": domain.JSONString(option.Label), "description": domain.JSONString(option.Description),
			})
		}
		object := map[string]domain.JSONValue{
			"question": domain.JSONString(question.Question), "header": domain.JSONString(question.Header),
			"options": domain.JSONArray(options),
		}
		contractOptionalBool(object, "multiple", question.Multiple)
		contractOptionalBool(object, "custom", question.Custom)
		questions[index] = domain.JSONObject(object)
	}
	object := map[string]domain.JSONValue{
		"id": domain.JSONString(string(request.ID)), "sessionID": domain.JSONString(string(request.SessionID)),
		"questions": domain.JSONArray(questions),
	}
	if request.Tool != nil {
		object["tool"] = domain.JSONObject(map[string]domain.JSONValue{
			"messageID": domain.JSONString(request.Tool.MessageID), "callID": domain.JSONString(request.Tool.CallID),
		})
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeQuestionRequestJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded question request: %w", err)
	}
	return encoded, nil
}

func decodeQuestionInfo(value domain.JSONValue, index int) (domain.QuestionInfo, error) {
	if value.Kind != domain.JSONKindObject {
		return domain.QuestionInfo{}, fmt.Errorf("question %d must be an object", index)
	}
	object := value.Object
	if err := rejectUnknownContractFields(object, "question", "question", "header", "options", "multiple", "custom"); err != nil {
		return domain.QuestionInfo{}, err
	}
	question, err := requiredContractString(object, "question", "question")
	if err != nil {
		return domain.QuestionInfo{}, err
	}
	header, err := requiredContractString(object, "header", "question")
	if err != nil {
		return domain.QuestionInfo{}, err
	}
	optionValues, err := requiredContractArray(object, "options", "question")
	if err != nil {
		return domain.QuestionInfo{}, err
	}
	options := make([]domain.QuestionOption, len(optionValues))
	for optionIndex, optionValue := range optionValues {
		if optionValue.Kind != domain.JSONKindObject {
			return domain.QuestionInfo{}, fmt.Errorf("question option %d must be an object", optionIndex)
		}
		if err := rejectUnknownContractFields(optionValue.Object, "question option", "label", "description"); err != nil {
			return domain.QuestionInfo{}, err
		}
		label, err := requiredContractString(optionValue.Object, "label", "question option")
		if err != nil {
			return domain.QuestionInfo{}, err
		}
		description, err := requiredContractString(optionValue.Object, "description", "question option")
		if err != nil {
			return domain.QuestionInfo{}, err
		}
		options[optionIndex] = domain.QuestionOption{Label: label, Description: description}
	}
	multiple, err := optionalContractBool(object, "multiple", "question")
	if err != nil {
		return domain.QuestionInfo{}, err
	}
	custom, err := optionalContractBool(object, "custom", "question")
	if err != nil {
		return domain.QuestionInfo{}, err
	}
	return domain.QuestionInfo{Question: question, Header: header, Options: options, Multiple: multiple, Custom: custom}, nil
}

func decodeQuestionTool(object map[string]domain.JSONValue) (*domain.QuestionTool, error) {
	toolObject, present, err := optionalContractObject(object, "tool", "question request")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(toolObject, "question tool", "messageID", "callID"); err != nil {
		return nil, err
	}
	messageID, err := requiredContractString(toolObject, "messageID", "question tool")
	if err != nil {
		return nil, err
	}
	callID, err := requiredContractString(toolObject, "callID", "question tool")
	if err != nil {
		return nil, err
	}
	return &domain.QuestionTool{MessageID: messageID, CallID: callID}, nil
}

func DecodeQuestionReplyJSON(content []byte) (domain.QuestionReply, error) {
	object, err := decodeContractObject(content, "question reply")
	if err != nil {
		return domain.QuestionReply{}, err
	}
	if err := rejectUnknownContractFields(object, "question reply", "answers"); err != nil {
		return domain.QuestionReply{}, err
	}
	answerValues, err := requiredContractArray(object, "answers", "question reply")
	if err != nil {
		return domain.QuestionReply{}, err
	}
	answers := make([]domain.QuestionAnswer, len(answerValues))
	for index, value := range answerValues {
		answer, err := decodeContractStringArray(value, fmt.Sprintf("question answer %d", index))
		if err != nil {
			return domain.QuestionReply{}, err
		}
		answers[index] = domain.QuestionAnswer(answer)
	}
	return domain.QuestionReply{Answers: answers}, nil
}

func EncodeQuestionReplyJSON(reply domain.QuestionReply) ([]byte, error) {
	answers := make([]domain.JSONValue, len(reply.Answers))
	for index, answer := range reply.Answers {
		answers[index] = contractStringArray(answer)
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(map[string]domain.JSONValue{"answers": domain.JSONArray(answers)}))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeQuestionReplyJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded question reply: %w", err)
	}
	return encoded, nil
}
