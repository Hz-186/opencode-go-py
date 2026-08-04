package domain

type QuestionOption struct {
	Label       string
	Description string
}

type QuestionInfo struct {
	Question string
	Header   string
	Options  []QuestionOption
	Multiple *bool
	Custom   *bool
}

type QuestionTool struct {
	MessageID string
	CallID    string
}

type QuestionRequest struct {
	ID        QuestionID
	SessionID SessionID
	Questions []QuestionInfo
	Tool      *QuestionTool
}

type QuestionAnswer []string

type QuestionReply struct {
	Answers []QuestionAnswer
}
