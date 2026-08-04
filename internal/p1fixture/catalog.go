package p1fixture

import "fmt"

func Catalog() []Definition {
	definitions := []Definition{
		{Contract: "json-value", Name: "unicode-large-negative-zero", SourcePath: "packages/llm/test/schema.test.ts", Input: `{"large":9007199254740993,"negativeZero":-0,"text":"你好"}`},
		{Contract: "location-ref", Name: "workspace", SourcePath: "packages/schema/src/location.ts", Input: `{"directory":"/tmp/项目","workspaceID":"wrk_test"}`},
		{Contract: "sessions-cursor", Name: "frozen-vector", SourcePath: "packages/protocol/test/session-cursor.test.ts", Input: "eyJvcmRlciI6ImRlc2MiLCJzZWFyY2giOiJwcm90b2NvbCIsImFuY2hvciI6eyJpZCI6InNlc190ZXN0IiwidGltZSI6MSwiZGlyZWN0aW9uIjoibmV4dCJ9fQ"},
		{Contract: "llm-usage", Name: "complete", SourcePath: "packages/llm/src/schema/events.ts", Input: `{"inputTokens":3,"outputTokens":2,"nonCachedInputTokens":1,"cacheReadInputTokens":1,"cacheWriteInputTokens":1,"reasoningTokens":1,"totalTokens":5,"providerMetadata":{"openai":{"large":9007199254740993}}}`},
		{Contract: "permission-request", Name: "complete", SourcePath: "packages/schema/src/permission.ts", Input: `{"id":"per_test","sessionID":"ses_test","action":"write","resources":["src/**"],"save":[],"metadata":{"large":9007199254740993},"source":{"type":"tool","messageID":"msg_test","callID":"call_1"}}`},
		{Contract: "permission-reply", Name: "always", SourcePath: "packages/schema/src/permission.ts", Input: `"always"`},
		{Contract: "permission-ruleset", Name: "allow-ask", SourcePath: "packages/schema/src/permission.ts", Input: `[{"action":"read","resource":"src/**","effect":"allow"},{"action":"write","resource":"*","effect":"ask"}]`},
		{Contract: "question-request", Name: "complete", SourcePath: "packages/schema/src/question.ts", Input: `{"id":"que_test","sessionID":"ses_test","questions":[{"question":"选择模型？","header":"模型","options":[{"label":"快速","description":"低延迟"}],"multiple":false,"custom":true}],"tool":{"messageID":"msg_test","callID":"call_1"}}`},
		{Contract: "question-reply", Name: "answers", SourcePath: "packages/schema/src/question.ts", Input: `{"answers":[["快速"],["自定义答案"]]}`},
		{Contract: "event-envelope", Name: "durable-location", SourcePath: "packages/schema/src/event.ts", Input: `{"id":"evt_test","type":"future.event","data":{"large":9007199254740993},"durable":{"aggregateID":"ses_test","seq":9007199254740993,"version":1},"location":{"directory":"/tmp","workspaceID":"wrk_test"},"metadata":{"trace":{"large":9007199254740993}}}`},
	}
	definitions = append(definitions, llmEventDefinitions()...)
	definitions = append(definitions, Definition{
		Contract: "llm-message", Name: "all-content", SourcePath: "packages/llm/src/schema/messages.ts",
		Input: `{"id":"llm_msg_1","role":"user","content":[{"type":"text","text":"你好","cache":{"type":"ephemeral","ttlSeconds":60}},{"type":"media","mediaType":"image/png","data":"AAECAw==","filename":"图.png"},{"type":"tool-call","id":"call_1","name":"lookup","input":{"large":9007199254740993}},{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"json","value":{"ok":true}}},{"type":"reasoning","text":"think","encrypted":"cipher"}]}`,
	})
	definitions = append(definitions, llmFailureDefinitions()...)
	definitions = append(definitions, sessionMessageDefinitions()...)
	definitions = append(definitions, sessionEventDefinitions()...)
	return definitions
}

func llmEventDefinitions() []Definition {
	const source = "packages/llm/src/schema/events.ts"
	values := []struct{ name, input string }{
		{"step-start", `{"type":"step-start","index":0}`},
		{"text-start", `{"type":"text-start","id":"text_1"}`},
		{"text-delta", `{"type":"text-delta","id":"text_1","text":"你"}`},
		{"text-end", `{"type":"text-end","id":"text_1"}`},
		{"reasoning-start", `{"type":"reasoning-start","id":"reason_1"}`},
		{"reasoning-delta", `{"type":"reasoning-delta","id":"reason_1","text":"think"}`},
		{"reasoning-end", `{"type":"reasoning-end","id":"reason_1"}`},
		{"tool-input-start", `{"type":"tool-input-start","id":"call_1","name":"lookup"}`},
		{"tool-input-delta", `{"type":"tool-input-delta","id":"call_1","name":"lookup","text":"{}"}`},
		{"tool-input-end", `{"type":"tool-input-end","id":"call_1","name":"lookup"}`},
		{"tool-call", `{"type":"tool-call","id":"call_1","name":"lookup","input":{"id":9007199254740993}}`},
		{"tool-result", `{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"content","value":[{"type":"text","text":"ok"}]},"output":{"structured":{"ok":true},"content":[]},"providerExecuted":false}`},
		{"tool-error", `{"type":"tool-error","id":"call_1","name":"lookup","message":"failed","error":{"code":"E"}}`},
		{"step-finish", `{"type":"step-finish","index":0,"reason":"tool-calls","usage":{"inputTokens":3}}`},
		{"finish", `{"type":"finish","reason":"stop","usage":{"outputTokens":2}}`},
		{"provider-error", `{"type":"provider-error","message":"overflow","classification":"context-overflow","retryable":false}`},
	}
	result := make([]Definition, len(values))
	for index, value := range values {
		result[index] = Definition{Contract: "llm-event", Name: value.name, SourcePath: source, Input: value.input}
	}
	return result
}

func llmFailureDefinitions() []Definition {
	const source = "packages/llm/src/schema/errors.ts"
	values := []struct{ name, input string }{
		{"invalid-request", `{"module":"provider","method":"stream","reason":{"_tag":"InvalidRequest","message":"bad","parameter":"model","classification":"context-overflow"}}`},
		{"no-route", `{"module":"provider","method":"route","reason":{"_tag":"NoRoute","route":"responses","provider":"openai","model":"gpt-test"}}`},
		{"authentication", `{"module":"provider","method":"stream","reason":{"_tag":"Authentication","message":"bad key","kind":"invalid"}}`},
		{"rate-limit", `{"module":"provider","method":"stream","reason":{"_tag":"RateLimit","message":"slow","retryAfterMs":1000,"http":{"request":{"method":"POST","url":"https://example.test","headers":{}},"response":{"status":429,"headers":{}}}}}`},
		{"quota-exceeded", `{"module":"provider","method":"stream","reason":{"_tag":"QuotaExceeded","message":"quota"}}`},
		{"content-policy", `{"module":"provider","method":"stream","reason":{"_tag":"ContentPolicy","message":"blocked"}}`},
		{"provider-internal", `{"module":"provider","method":"stream","reason":{"_tag":"ProviderInternal","message":"down","status":503}}`},
		{"transport", `{"module":"provider","method":"stream","reason":{"_tag":"Transport","message":"reset","kind":"ECONNRESET"}}`},
		{"invalid-provider-output", `{"module":"provider","method":"stream","reason":{"_tag":"InvalidProviderOutput","message":"bad frame","raw":"{}"}}`},
		{"unknown-provider", `{"module":"provider","method":"stream","reason":{"_tag":"UnknownProvider","message":"unknown","status":599}}`},
	}
	result := make([]Definition, len(values))
	for index, value := range values {
		result[index] = Definition{Contract: "llm-failure", Name: value.name, SourcePath: source, Input: value.input}
	}
	return result
}

func sessionMessageDefinitions() []Definition {
	const source = "packages/schema/src/session-message.ts"
	values := []struct{ name, input string }{
		{"agent-switched", `{"id":"msg_agent","type":"agent-switched","agent":"build","time":{"created":1}}`},
		{"model-switched", `{"id":"msg_model","type":"model-switched","model":{"id":"m","providerID":"p","variant":"fast"},"time":{"created":2}}`},
		{"user", `{"id":"msg_user","type":"user","text":"检查","files":[{"uri":"file:///tmp/a","mime":"text/plain"}],"agents":[{"name":"review"}],"time":{"created":3}}`},
		{"synthetic", `{"id":"msg_synthetic","type":"synthetic","sessionID":"ses_test","text":"synthetic","time":{"created":4}}`},
		{"system", `{"id":"msg_system","type":"system","text":"update","time":{"created":5}}`},
		{"shell", `{"id":"msg_shell","type":"shell","callID":"call_1","command":"pwd","output":"/tmp","time":{"created":6,"completed":7}}`},
		{"assistant-running", `{"id":"msg_assistant","type":"assistant","agent":"build","model":{"id":"m","providerID":"p"},"content":[{"type":"text","id":"text_1","text":"done"},{"type":"reasoning","id":"reason_1","text":"think"},{"type":"tool","id":"call_1","name":"lookup","state":{"status":"running","input":{"large":9007199254740993},"structured":{},"content":[]},"time":{"created":1}}],"time":{"created":1}}`},
		{"assistant-pending", `{"id":"msg_pending","type":"assistant","agent":"build","model":{"id":"m","providerID":"p"},"content":[{"type":"tool","id":"call_1","name":"lookup","state":{"status":"pending","input":"{}"},"time":{"created":1}}],"time":{"created":1}}`},
		{"assistant-completed", `{"id":"msg_completed","type":"assistant","agent":"build","model":{"id":"m","providerID":"p"},"content":[{"type":"tool","id":"call_1","name":"lookup","state":{"status":"completed","input":{},"content":[],"structured":{},"result":{"large":9007199254740993}},"time":{"created":1,"completed":2}}],"time":{"created":1}}`},
		{"assistant-error", `{"id":"msg_error","type":"assistant","agent":"build","model":{"id":"m","providerID":"p"},"content":[{"type":"tool","id":"call_1","name":"lookup","state":{"status":"error","input":{},"content":[],"structured":{},"error":{"type":"unknown","message":"failed"}},"time":{"created":1}}],"time":{"created":1}}`},
		{"compaction", `{"id":"msg_compaction","type":"compaction","reason":"auto","summary":"summary","recent":"recent","time":{"created":13}}`},
	}
	result := make([]Definition, len(values))
	for index, value := range values {
		result[index] = Definition{Contract: "session-message", Name: value.name, SourcePath: source, Input: value.input}
	}
	return result
}

func sessionEventDefinitions() []Definition {
	const source = "packages/schema/src/session-event.ts"
	base := `"timestamp":1,"sessionID":"ses_test"`
	values := []struct{ name, data string }{
		{"session.next.agent.switched", base + `,"messageID":"msg_test","agent":"build"`},
		{"session.next.model.switched", base + `,"messageID":"msg_test","model":{"id":"m","providerID":"p"}`},
		{"session.next.moved", base + `,"location":{"directory":"/tmp"}`},
		{"session.next.prompted", base + `,"messageID":"msg_test","prompt":{"text":"hello"},"delivery":"steer"`},
		{"session.next.prompt.admitted", base + `,"messageID":"msg_test","prompt":{"text":"hello"},"delivery":"queue"`},
		{"session.next.context.updated", base + `,"messageID":"msg_test","text":"context"`},
		{"session.next.synthetic", base + `,"messageID":"msg_test","text":"synthetic"`},
		{"session.next.shell.started", base + `,"messageID":"msg_test","callID":"call_1","command":"pwd"`},
		{"session.next.shell.ended", base + `,"callID":"call_1","output":"/tmp"`},
		{"session.next.step.started", base + `,"assistantMessageID":"msg_test","agent":"build","model":{"id":"m","providerID":"p"}`},
		{"session.next.step.ended", base + `,"assistantMessageID":"msg_test","finish":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}`},
		{"session.next.step.failed", base + `,"assistantMessageID":"msg_test","error":{"type":"unknown","message":"failed"}`},
		{"session.next.text.started", base + `,"assistantMessageID":"msg_test","textID":"text_1"`},
		{"session.next.text.delta", base + `,"assistantMessageID":"msg_test","textID":"text_1","delta":"a"`},
		{"session.next.text.ended", base + `,"assistantMessageID":"msg_test","textID":"text_1","text":"all"`},
		{"session.next.reasoning.started", base + `,"assistantMessageID":"msg_test","reasoningID":"reason_1"`},
		{"session.next.reasoning.delta", base + `,"assistantMessageID":"msg_test","reasoningID":"reason_1","delta":"a"`},
		{"session.next.reasoning.ended", base + `,"assistantMessageID":"msg_test","reasoningID":"reason_1","text":"all"`},
		{"session.next.tool.input.started", base + `,"assistantMessageID":"msg_test","callID":"call_1","name":"lookup"`},
		{"session.next.tool.input.delta", base + `,"assistantMessageID":"msg_test","callID":"call_1","delta":"{}"`},
		{"session.next.tool.input.ended", base + `,"assistantMessageID":"msg_test","callID":"call_1","text":"{}"`},
		{"session.next.tool.called", base + `,"assistantMessageID":"msg_test","callID":"call_1","tool":"lookup","input":{},"provider":{"executed":false}`},
		{"session.next.tool.progress", base + `,"assistantMessageID":"msg_test","callID":"call_1","structured":{},"content":[]`},
		{"session.next.tool.success", base + `,"assistantMessageID":"msg_test","callID":"call_1","structured":{},"content":[],"provider":{"executed":true}`},
		{"session.next.tool.failed", base + `,"assistantMessageID":"msg_test","callID":"call_1","error":{"type":"unknown","message":"failed"},"provider":{"executed":true}`},
		{"session.next.retried", base + `,"attempt":1,"error":{"message":"retry","isRetryable":true}`},
		{"session.next.compaction.started", base + `,"messageID":"msg_test","reason":"auto"`},
		{"session.next.compaction.delta", base + `,"messageID":"msg_test","text":"part"`},
		{"session.next.compaction.ended", base + `,"messageID":"msg_test","reason":"manual","text":"summary","recent":"recent"`},
		{"session.next.revert.staged", base + `,"revert":{"messageID":"msg_test"}`},
		{"session.next.revert.cleared", base},
		{"session.next.revert.committed", base + `,"messageID":"msg_test"`},
	}
	result := make([]Definition, len(values))
	for index, value := range values {
		input := fmt.Sprintf(`{"id":"evt_test","type":%q,"data":{%s}}`, value.name, value.data)
		result[index] = Definition{Contract: "session-event", Name: value.name, SourcePath: source, Input: input}
	}
	return result
}
