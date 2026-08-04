package baseline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

type tsTokenKind uint8

const (
	tsIdentifier tsTokenKind = iota
	tsString
	tsTemplate
	tsNumber
	tsPunctuation
)

type tsToken struct {
	kind   tsTokenKind
	raw    string
	value  string
	offset int
	end    int
	line   int
}

type tsExpression struct {
	tokens []tsToken
	source []byte
}

type tsExport struct {
	symbol       string
	targetSymbol string
	kind         string
	sourcePath   string
	targetPath   string
	line         int
	star         bool
}

type namespaceSpan struct {
	name  string
	open  int
	close int
}

type eventDefinition struct {
	id               string
	modulePath       string
	localSymbol      string
	record           EventRecord
	durableAggregate string
	durableVersion   int
}

type containerRef struct {
	name   string
	filter int
}

type eventContainer struct {
	modulePath string
	name       string
	refs       []containerRef
}

type tsModule struct {
	path           string
	source         []byte
	tokens         []tsToken
	imports        map[string]string
	namespace      string
	reexportTarget string
	exports        []tsExport
	static         map[string]tsExpression
	events         []*eventDefinition
	containers     map[string]eventContainer
}

func scanTypeScript(source []byte) ([]tsToken, error) {
	tokens := make([]tsToken, 0, len(source)/5)
	line := 1
	for offset := 0; offset < len(source); {
		start := offset
		switch source[offset] {
		case ' ', '\t', '\r', '\n':
			for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t' || source[offset] == '\r' || source[offset] == '\n') {
				if source[offset] == '\n' {
					line++
				}
				offset++
			}
			continue
		case '/':
			if offset+1 < len(source) && source[offset+1] == '/' {
				offset += 2
				for offset < len(source) && source[offset] != '\n' {
					offset++
				}
				continue
			}
			if offset+1 < len(source) && source[offset+1] == '*' {
				offset += 2
				closed := false
				for offset+1 < len(source) {
					if source[offset] == '\n' {
						line++
					}
					if source[offset] == '*' && source[offset+1] == '/' {
						offset += 2
						closed = true
						break
					}
					offset++
				}
				if !closed {
					return nil, fmt.Errorf("unterminated TypeScript block comment at line %d", line)
				}
				continue
			}
		case '\'', '"':
			end, ok := scanQuoted(source, offset, source[offset])
			if !ok {
				return nil, fmt.Errorf("unterminated TypeScript string at line %d", line)
			}
			raw := string(source[offset:end])
			value, ok := decodeTSQuoted(raw)
			if !ok {
				return nil, fmt.Errorf("invalid TypeScript string at line %d", line)
			}
			tokens = append(tokens, tsToken{kind: tsString, raw: raw, value: value, offset: offset, end: end, line: line})
			line += bytes.Count(source[offset:end], []byte{'\n'})
			offset = end
			continue
		case '`':
			end, ok := scanTemplate(source, offset)
			if !ok {
				return nil, fmt.Errorf("unterminated TypeScript template at line %d", line)
			}
			raw := string(source[offset:end])
			tokens = append(tokens, tsToken{kind: tsTemplate, raw: raw, offset: offset, end: end, line: line})
			line += bytes.Count(source[offset:end], []byte{'\n'})
			offset = end
			continue
		}

		if isTSIdentifierStart(source[offset]) {
			offset++
			for offset < len(source) && isTSIdentifierContinue(source[offset]) {
				offset++
			}
			raw := string(source[start:offset])
			tokens = append(tokens, tsToken{kind: tsIdentifier, raw: raw, value: raw, offset: start, end: offset, line: line})
			continue
		}
		if source[offset] >= '0' && source[offset] <= '9' {
			offset++
			for offset < len(source) && ((source[offset] >= '0' && source[offset] <= '9') || source[offset] == '.') {
				offset++
			}
			raw := string(source[start:offset])
			tokens = append(tokens, tsToken{kind: tsNumber, raw: raw, value: raw, offset: start, end: offset, line: line})
			continue
		}
		if offset+2 < len(source) && string(source[offset:offset+3]) == "..." {
			tokens = append(tokens, tsToken{kind: tsPunctuation, raw: "...", value: "...", offset: offset, end: offset + 3, line: line})
			offset += 3
			continue
		}
		raw := string(source[offset : offset+1])
		tokens = append(tokens, tsToken{kind: tsPunctuation, raw: raw, value: raw, offset: offset, end: offset + 1, line: line})
		offset++
	}
	return tokens, nil
}

func scanQuoted(source []byte, start int, quote byte) (int, bool) {
	for offset := start + 1; offset < len(source); offset++ {
		if source[offset] == '\\' {
			offset++
			continue
		}
		if source[offset] == quote {
			return offset + 1, true
		}
	}
	return 0, false
}

func scanTemplate(source []byte, start int) (int, bool) {
	for offset := start + 1; offset < len(source); offset++ {
		if source[offset] == '\\' {
			offset++
			continue
		}
		if source[offset] == '`' {
			return offset + 1, true
		}
		if source[offset] == '$' && offset+1 < len(source) && source[offset+1] == '{' {
			end, ok := scanTemplateExpression(source, offset+2)
			if !ok {
				return 0, false
			}
			offset = end - 1
		}
	}
	return 0, false
}

func scanTemplateExpression(source []byte, start int) (int, bool) {
	depth := 1
	for offset := start; offset < len(source); offset++ {
		switch source[offset] {
		case '\'', '"':
			end, ok := scanQuoted(source, offset, source[offset])
			if !ok {
				return 0, false
			}
			offset = end - 1
		case '`':
			end, ok := scanTemplate(source, offset)
			if !ok {
				return 0, false
			}
			offset = end - 1
		case '/':
			if offset+1 < len(source) && source[offset+1] == '/' {
				offset += 2
				for offset < len(source) && source[offset] != '\n' {
					offset++
				}
			}
			if offset+1 < len(source) && source[offset+1] == '*' {
				offset += 2
				for offset+1 < len(source) && !(source[offset] == '*' && source[offset+1] == '/') {
					offset++
				}
				offset++
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return offset + 1, true
			}
		}
	}
	return 0, false
}

func decodeTSQuoted(raw string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	return unescapeTS(raw[1 : len(raw)-1])
}

func unescapeTS(value string) (string, bool) {
	var result strings.Builder
	for offset := 0; offset < len(value); offset++ {
		if value[offset] != '\\' {
			result.WriteByte(value[offset])
			continue
		}
		offset++
		if offset >= len(value) {
			return "", false
		}
		switch value[offset] {
		case '\\', '\'', '"', '`', '$':
			result.WriteByte(value[offset])
		case 'n':
			result.WriteByte('\n')
		case 'r':
			result.WriteByte('\r')
		case 't':
			result.WriteByte('\t')
		case '\n':
		case 'x':
			if offset+2 >= len(value) {
				return "", false
			}
			parsed, err := strconv.ParseUint(value[offset+1:offset+3], 16, 8)
			if err != nil {
				return "", false
			}
			result.WriteByte(byte(parsed))
			offset += 2
		case 'u':
			if offset+4 >= len(value) {
				return "", false
			}
			parsed, err := strconv.ParseUint(value[offset+1:offset+5], 16, 16)
			if err != nil {
				return "", false
			}
			result.WriteRune(rune(parsed))
			offset += 4
		default:
			result.WriteByte(value[offset])
		}
	}
	return result.String(), true
}

func isTSIdentifierStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isTSIdentifierContinue(value byte) bool {
	return isTSIdentifierStart(value) || value >= '0' && value <= '9'
}

func matchToken(tokens []tsToken, start int, open string, close string) int {
	if start < 0 || start >= len(tokens) || tokens[start].raw != open {
		return -1
	}
	depth := 0
	for index := start; index < len(tokens); index++ {
		switch tokens[index].raw {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func splitTopLevel(tokens []tsToken, separator string) [][]tsToken {
	result := make([][]tsToken, 0)
	start := 0
	paren, brace, bracket := 0, 0, 0
	for index, token := range tokens {
		switch token.raw {
		case "(":
			paren++
		case ")":
			paren--
		case "{":
			brace++
		case "}":
			brace--
		case "[":
			bracket++
		case "]":
			bracket--
		default:
			if token.raw == separator && paren == 0 && brace == 0 && bracket == 0 {
				result = append(result, trimTokens(tokens[start:index]))
				start = index + 1
			}
		}
	}
	result = append(result, trimTokens(tokens[start:]))
	return result
}

func trimTokens(tokens []tsToken) []tsToken {
	for len(tokens) > 0 && (tokens[0].raw == "," || tokens[0].raw == ";") {
		tokens = tokens[1:]
	}
	for len(tokens) > 0 && (tokens[len(tokens)-1].raw == "," || tokens[len(tokens)-1].raw == ";") {
		tokens = tokens[:len(tokens)-1]
	}
	for len(tokens) >= 2 && tokens[len(tokens)-2].raw == "as" && tokens[len(tokens)-1].raw == "const" {
		tokens = tokens[:len(tokens)-2]
	}
	for len(tokens) >= 2 && tokens[0].raw == "(" && matchToken(tokens, 0, "(", ")") == len(tokens)-1 {
		tokens = tokens[1 : len(tokens)-1]
	}
	return tokens
}

func rawTokens(source []byte, tokens []tsToken) string {
	if len(tokens) == 0 {
		return ""
	}
	return strings.TrimSpace(string(source[tokens[0].offset:tokens[len(tokens)-1].end]))
}

func collectStaticExpressions(source []byte, tokens []tsToken) map[string]tsExpression {
	result := make(map[string]tsExpression)
	for index := 0; index+3 < len(tokens); index++ {
		if tokens[index].raw != "const" || tokens[index+1].kind != tsIdentifier || tokens[index+2].raw != "=" {
			continue
		}
		name := tokens[index+1].raw
		start := index + 3
		end := expressionEnd(tokens, start)
		if end <= start {
			continue
		}
		expression := trimTokens(tokens[start:end])
		result[name] = tsExpression{tokens: expression, source: source}
		if len(expression) > 1 && expression[0].raw == "{" {
			close := matchToken(expression, 0, "{", "}")
			if close > 0 {
				for key, value := range objectProperties(expression[1:close]) {
					result[name+"."+key] = tsExpression{tokens: value, source: source}
				}
			}
		}
	}
	return result
}

func expressionEnd(tokens []tsToken, start int) int {
	if start >= len(tokens) {
		return start
	}
	switch tokens[start].raw {
	case "{":
		if close := matchToken(tokens, start, "{", "}"); close >= 0 {
			return close + 1
		}
	case "[":
		if close := matchToken(tokens, start, "[", "]"); close >= 0 {
			return close + 1
		}
	}
	if tokens[start].kind == tsString || tokens[start].kind == tsTemplate {
		return start + 1
	}
	paren, brace, bracket := 0, 0, 0
	startLine := tokens[start].line
	for index := start; index < len(tokens); index++ {
		switch tokens[index].raw {
		case "(":
			paren++
		case ")":
			if paren == 0 {
				return index
			}
			paren--
		case "{":
			brace++
		case "}":
			if brace == 0 {
				return index
			}
			brace--
		case "[":
			bracket++
		case "]":
			if bracket == 0 {
				return index
			}
			bracket--
		case ",", ";":
			if paren == 0 && brace == 0 && bracket == 0 {
				return index
			}
		}
		if index > start && tokens[index].line > startLine && paren == 0 && brace == 0 && bracket == 0 {
			return index
		}
	}
	return len(tokens)
}

func objectProperties(tokens []tsToken) map[string][]tsToken {
	result := make(map[string][]tsToken)
	for _, field := range splitTopLevel(tokens, ",") {
		colon := topLevelToken(field, ":")
		if colon < 1 {
			continue
		}
		key := field[0].value
		if field[0].kind != tsIdentifier && field[0].kind != tsString {
			continue
		}
		result[key] = trimTokens(field[colon+1:])
	}
	return result
}

func topLevelToken(tokens []tsToken, want string) int {
	paren, brace, bracket := 0, 0, 0
	for index, token := range tokens {
		if token.raw == want && paren == 0 && brace == 0 && bracket == 0 {
			return index
		}
		switch token.raw {
		case "(":
			paren++
		case ")":
			paren--
		case "{":
			brace++
		case "}":
			brace--
		case "[":
			bracket++
		case "]":
			bracket--
		}
	}
	return -1
}

func evaluateStatic(expression []tsToken, source []byte, environment map[string]tsExpression, visiting map[string]bool) (string, bool) {
	expression = trimTokens(expression)
	if len(expression) == 0 {
		return "", false
	}
	if len(expression) == 1 {
		switch expression[0].kind {
		case tsString:
			return expression[0].value, true
		case tsTemplate:
			return evaluateTemplate(expression[0].raw, environment, visiting)
		}
	}
	parts := splitTopLevel(expression, "+")
	if len(parts) > 1 {
		var result strings.Builder
		for _, part := range parts {
			value, ok := evaluateStatic(part, source, environment, visiting)
			if !ok {
				return "", false
			}
			result.WriteString(value)
		}
		return result.String(), true
	}
	name, ok := dottedName(expression)
	if !ok || visiting[name] {
		return "", false
	}
	value, found := environment[name]
	if !found {
		return "", false
	}
	visiting[name] = true
	resolved, ok := evaluateStatic(value.tokens, value.source, environment, visiting)
	delete(visiting, name)
	return resolved, ok
}

func evaluateTemplate(raw string, environment map[string]tsExpression, visiting map[string]bool) (string, bool) {
	if len(raw) < 2 || raw[0] != '`' || raw[len(raw)-1] != '`' {
		return "", false
	}
	content := raw[1 : len(raw)-1]
	var result strings.Builder
	for offset := 0; offset < len(content); {
		marker := strings.Index(content[offset:], "${")
		if marker < 0 {
			value, ok := unescapeTS(content[offset:])
			if !ok {
				return "", false
			}
			result.WriteString(value)
			break
		}
		marker += offset
		literal, ok := unescapeTS(content[offset:marker])
		if !ok {
			return "", false
		}
		result.WriteString(literal)
		end, ok := scanTemplateExpression([]byte(content), marker+2)
		if !ok {
			return "", false
		}
		inner := []byte(content[marker+2 : end-1])
		tokens, err := scanTypeScript(inner)
		if err != nil {
			return "", false
		}
		value, ok := evaluateStatic(tokens, inner, environment, visiting)
		if !ok {
			return "", false
		}
		result.WriteString(value)
		offset = end
	}
	return result.String(), true
}

func dottedName(tokens []tsToken) (string, bool) {
	tokens = trimTokens(tokens)
	if len(tokens) == 0 || tokens[0].kind != tsIdentifier {
		return "", false
	}
	parts := []string{tokens[0].raw}
	for index := 1; index < len(tokens); index += 2 {
		if index+1 >= len(tokens) || tokens[index].raw != "." || tokens[index+1].kind != tsIdentifier {
			return "", false
		}
		parts = append(parts, tokens[index+1].raw)
	}
	return strings.Join(parts, "."), true
}

func extractRoutes(sources []semanticSource) ([]RouteRecord, error) {
	routes := make([]RouteRecord, 0)
	for _, file := range sources {
		if !isRouteSource(file.path) {
			continue
		}
		tokens, err := scanTypeScript(file.content)
		if err != nil {
			return nil, fmt.Errorf("parse routes in %s: %w", file.path, err)
		}
		environment := collectStaticExpressions(file.content, tokens)
		for index := 0; index+4 < len(tokens); index++ {
			if tokens[index].raw != "HttpApiEndpoint" || tokens[index+1].raw != "." || tokens[index+2].kind != tsIdentifier || tokens[index+3].raw != "(" {
				continue
			}
			method, supported := routeMethod(tokens[index+2].raw)
			if !supported {
				continue
			}
			close := matchToken(tokens, index+3, "(", ")")
			if close < 0 {
				return nil, fmt.Errorf("unbalanced HttpApiEndpoint.%s in %s:%d", tokens[index+2].raw, file.path, tokens[index].line)
			}
			arguments := splitTopLevel(tokens[index+4:close], ",")
			if len(arguments) < 2 {
				return nil, fmt.Errorf("HttpApiEndpoint.%s in %s:%d has fewer than two arguments", tokens[index+2].raw, file.path, tokens[index].line)
			}
			operation, operationOK := evaluateStatic(arguments[0], file.content, environment, make(map[string]bool))
			if !operationOK {
				operation = rawTokens(file.content, arguments[0])
			}
			pathExpression := rawTokens(file.content, arguments[1])
			resolvedPath, resolved := evaluateStatic(arguments[1], file.content, environment, make(map[string]bool))
			status := PathUnresolved
			if resolved {
				status = PathResolved
			}
			next := len(tokens)
			for cursor := close + 1; cursor+3 < len(tokens); cursor++ {
				if tokens[cursor].raw == "HttpApiEndpoint" && tokens[cursor+1].raw == "." {
					next = cursor
					break
				}
			}
			routes = append(routes, RouteRecord{
				OperationID: operation, Method: method, PathExpression: pathExpression, Path: resolvedPath,
				PathStatus: status, OpenAPIIdentifier: findOpenAPIIdentifier(tokens[close+1 : next]),
				SourcePath: file.path, Line: tokens[index].line, Classification: semanticScope(file.path),
			})
			index = close
		}
	}
	return routes, nil
}

func routeMethod(method string) (string, bool) {
	switch method {
	case "get":
		return "GET", true
	case "post":
		return "POST", true
	case "put":
		return "PUT", true
	case "patch":
		return "PATCH", true
	case "delete", "del":
		return "DELETE", true
	default:
		return "", false
	}
}

func findOpenAPIIdentifier(tokens []tsToken) string {
	for index := 0; index+2 < len(tokens); index++ {
		if tokens[index].raw == "identifier" && tokens[index+1].raw == ":" && tokens[index+2].kind == tsString {
			return tokens[index+2].value
		}
	}
	return ""
}

func buildSchemaModules(sources []semanticSource) (map[string]*tsModule, error) {
	modules := make(map[string]*tsModule)
	for _, file := range sources {
		if !strings.HasPrefix(file.path, "packages/schema/src/") || !strings.HasSuffix(file.path, ".ts") {
			continue
		}
		tokens, err := scanTypeScript(file.content)
		if err != nil {
			return nil, fmt.Errorf("parse schema module %s: %w", file.path, err)
		}
		modules[file.path] = &tsModule{
			path: file.path, source: file.content, tokens: tokens, imports: make(map[string]string),
			static: collectStaticExpressions(file.content, tokens), containers: make(map[string]eventContainer),
		}
	}
	for _, module := range modules {
		parseModuleImports(module, modules)
		module.exports = parseModuleExports(module, modules)
		module.namespace = moduleNamespace(module)
		module.reexportTarget = moduleReexportTarget(module, modules)
	}
	return modules, nil
}

func parseModuleImports(module *tsModule, modules map[string]*tsModule) {
	tokens := module.tokens
	for index := 0; index < len(tokens); index++ {
		if tokens[index].raw != "import" {
			continue
		}
		from := index + 1
		for from < len(tokens) && tokens[from].raw != "from" && tokens[from].line <= tokens[index].line+10 {
			from++
		}
		if from+1 >= len(tokens) || tokens[from].raw != "from" || tokens[from+1].kind != tsString {
			continue
		}
		target := resolveModulePath(module.path, tokens[from+1].value, modules)
		if target == "" {
			continue
		}
		open := index + 1
		if open < from && tokens[open].raw == "type" {
			open++
		}
		if open < from && tokens[open].raw == "{" {
			close := matchToken(tokens, open, "{", "}")
			if close > open && close < from {
				for _, item := range splitTopLevel(tokens[open+1:close], ",") {
					if len(item) == 0 || item[0].kind != tsIdentifier {
						continue
					}
					local := item[0].raw
					if len(item) >= 3 && item[1].raw == "as" && item[2].kind == tsIdentifier {
						local = item[2].raw
					}
					module.imports[local] = target
				}
			}
		}
	}
}

func resolveModulePath(sourcePath string, specifier string, modules map[string]*tsModule) string {
	if !strings.HasPrefix(specifier, ".") {
		return ""
	}
	resolved := path.Clean(path.Join(path.Dir(sourcePath), specifier))
	candidates := []string{resolved, resolved + ".ts", path.Join(resolved, "index.ts")}
	for _, candidate := range candidates {
		if _, exists := modules[candidate]; exists {
			return candidate
		}
	}
	return ""
}

func parseModuleExports(module *tsModule, modules map[string]*tsModule) []tsExport {
	result := make([]tsExport, 0)
	depth := 0
	for index := 0; index < len(module.tokens); index++ {
		token := module.tokens[index]
		if token.raw == "}" {
			depth--
		}
		if depth == 0 && token.raw == "export" && index+1 < len(module.tokens) {
			next := index + 1
			if module.tokens[next].raw == "declare" || module.tokens[next].raw == "async" {
				next++
			}
			if next < len(module.tokens) && isExportDeclarationKind(module.tokens[next].raw) && next+1 < len(module.tokens) && module.tokens[next+1].kind == tsIdentifier {
				result = append(result, tsExport{symbol: module.tokens[next+1].raw, targetSymbol: module.tokens[next+1].raw, kind: module.tokens[next].raw, sourcePath: module.path, line: module.tokens[next+1].line})
			}
			if next < len(module.tokens) && module.tokens[next].raw == "*" {
				if next+4 < len(module.tokens) && module.tokens[next+1].raw == "as" && module.tokens[next+2].kind == tsIdentifier && module.tokens[next+3].raw == "from" && module.tokens[next+4].kind == tsString {
					result = append(result, tsExport{symbol: module.tokens[next+2].raw, targetSymbol: "*", kind: "namespace", sourcePath: module.path, targetPath: resolveModulePath(module.path, module.tokens[next+4].value, modules), line: module.tokens[next+2].line})
				} else if next+2 < len(module.tokens) && module.tokens[next+1].raw == "from" && module.tokens[next+2].kind == tsString {
					result = append(result, tsExport{kind: "star", sourcePath: module.path, targetPath: resolveModulePath(module.path, module.tokens[next+2].value, modules), line: module.tokens[next].line, star: true})
				}
			}
			if next < len(module.tokens) && module.tokens[next].raw == "{" {
				close := matchToken(module.tokens, next, "{", "}")
				if close > next {
					target := ""
					if close+2 < len(module.tokens) && module.tokens[close+1].raw == "from" && module.tokens[close+2].kind == tsString {
						target = resolveModulePath(module.path, module.tokens[close+2].value, modules)
					}
					for _, item := range splitTopLevel(module.tokens[next+1:close], ",") {
						if len(item) == 0 || item[0].kind != tsIdentifier {
							continue
						}
						exported := item[0].raw
						if len(item) >= 3 && item[1].raw == "as" && item[2].kind == tsIdentifier {
							exported = item[2].raw
						}
						result = append(result, tsExport{symbol: exported, targetSymbol: item[0].raw, kind: "re-export", sourcePath: module.path, targetPath: target, line: item[0].line})
					}
				}
			}
		}
		if token.raw == "{" {
			depth++
		}
	}
	return result
}

func isExportDeclarationKind(value string) bool {
	switch value {
	case "const", "let", "var", "function", "class", "interface", "type", "enum", "namespace":
		return true
	default:
		return false
	}
}

func moduleNamespace(module *tsModule) string {
	base := strings.TrimSuffix(path.Base(module.path), ".ts")
	for _, exported := range module.exports {
		if exported.kind != "namespace" {
			continue
		}
		if exported.targetPath == module.path || exported.targetPath == "" && (exported.symbol == base || strings.EqualFold(exported.symbol, strings.ReplaceAll(base, "-", ""))) {
			return exported.symbol
		}
	}
	return pascalIdentifier(base)
}

func pascalIdentifier(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for index := range parts {
		if parts[index] != "" {
			parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
		}
	}
	return strings.Join(parts, "")
}

func moduleReexportTarget(module *tsModule, modules map[string]*tsModule) string {
	for _, exported := range module.exports {
		if exported.star && exported.targetPath != "" && exported.targetPath != module.path {
			return exported.targetPath
		}
	}
	return ""
}

func expandedExports(modules map[string]*tsModule, modulePath string, visiting map[string]bool) []tsExport {
	if visiting[modulePath] {
		return nil
	}
	module := modules[modulePath]
	if module == nil {
		return nil
	}
	visiting[modulePath] = true
	defer delete(visiting, modulePath)
	result := make([]tsExport, 0)
	for _, exported := range module.exports {
		if exported.star {
			result = append(result, expandedExports(modules, exported.targetPath, visiting)...)
			continue
		}
		resolved := exported
		if exported.targetPath != "" && exported.kind == "re-export" {
			for _, target := range expandedExports(modules, exported.targetPath, visiting) {
				if target.symbol != exported.targetSymbol {
					continue
				}
				resolved.kind = target.kind
				resolved.sourcePath = target.sourcePath
				resolved.line = target.line
				break
			}
		}
		result = append(result, resolved)
	}
	return result
}

func extractSchemaSurface(sources []semanticSource) ([]SchemaExportRecord, []PublicSymbolRecord, error) {
	modules, err := buildSchemaModules(sources)
	if err != nil {
		return nil, nil, err
	}
	var packageDocument struct {
		Name    string            `json:"name"`
		Exports map[string]string `json:"exports"`
	}
	foundPackage := false
	for _, file := range sources {
		if file.path != "packages/schema/package.json" {
			continue
		}
		foundPackage = true
		if err := json.Unmarshal(file.content, &packageDocument); err != nil {
			return nil, nil, fmt.Errorf("decode packages/schema/package.json: %w", err)
		}
	}
	if !foundPackage || packageDocument.Name == "" {
		return nil, nil, fmt.Errorf("packages/schema/package.json is missing or has no name")
	}
	if packageDocument.Exports["."] == "" || packageDocument.Exports["./*"] == "" {
		return nil, nil, fmt.Errorf("schema package must expose both . and ./* entrypoints")
	}

	rootPath := resolveExportTarget("packages/schema/package.json", packageDocument.Exports["."], modules)
	if rootPath == "" {
		return nil, nil, fmt.Errorf("resolve schema root export %q", packageDocument.Exports["."])
	}
	rootExports := expandedExports(modules, rootPath, make(map[string]bool))
	schemas := make([]SchemaExportRecord, 0, len(rootExports))
	schemaIndex := make(map[string]int, len(rootExports))
	symbols := make([]PublicSymbolRecord, 0)
	for _, exported := range rootExports {
		if exported.symbol == "" {
			continue
		}
		record := SchemaExportRecord{
			Package: packageDocument.Name, Entrypoint: ".", Symbol: exported.symbol, Kind: exported.kind,
			SourcePath: exported.sourcePath, Line: exported.line, Classification: semanticScope(exported.sourcePath),
		}
		key := record.Symbol + "\x00" + record.SourcePath
		if existing, found := schemaIndex[key]; found {
			schemas[existing].Kind = mergeExportKinds(schemas[existing].Kind, record.Kind)
			if record.Line < schemas[existing].Line {
				schemas[existing].Line = record.Line
			}
		} else {
			schemaIndex[key] = len(schemas)
			schemas = append(schemas, record)
		}
		symbols = append(symbols, PublicSymbolRecord{
			Package: record.Package, Entrypoint: record.Entrypoint, Symbol: record.Symbol, Kind: record.Kind,
			SourcePath: record.SourcePath, Line: record.Line, Classification: record.Classification,
		})
	}
	for modulePath := range modules {
		relative := strings.TrimSuffix(strings.TrimPrefix(modulePath, "packages/schema/src/"), ".ts")
		entrypoint := "./" + relative
		for _, exported := range expandedExports(modules, modulePath, make(map[string]bool)) {
			if exported.symbol == "" {
				continue
			}
			symbols = append(symbols, PublicSymbolRecord{
				Package: packageDocument.Name, Entrypoint: entrypoint, Symbol: exported.symbol, Kind: exported.kind,
				SourcePath: exported.sourcePath, Line: exported.line, Classification: semanticScope(exported.sourcePath),
			})
		}
	}
	return schemas, symbols, nil
}

func mergeExportKinds(current string, added string) string {
	kinds := strings.Split(current, "+")
	if !slices.Contains(kinds, added) {
		kinds = append(kinds, added)
	}
	slices.Sort(kinds)
	return strings.Join(kinds, "+")
}

func resolveExportTarget(packagePath string, target string, modules map[string]*tsModule) string {
	resolved := path.Clean(path.Join(path.Dir(packagePath), target))
	if _, exists := modules[resolved]; exists {
		return resolved
	}
	return ""
}

func namespaceSpans(tokens []tsToken) []namespaceSpan {
	spans := make([]namespaceSpan, 0)
	for index := 0; index+3 < len(tokens); index++ {
		if tokens[index].raw != "namespace" || tokens[index+1].kind != tsIdentifier || tokens[index+2].raw != "{" {
			continue
		}
		close := matchToken(tokens, index+2, "{", "}")
		if close > index+2 {
			spans = append(spans, namespaceSpan{name: tokens[index+1].raw, open: index + 2, close: close})
		}
	}
	return spans
}

func qualifiedAt(spans []namespaceSpan, tokenIndex int, name string) string {
	containing := make([]namespaceSpan, 0)
	for _, span := range spans {
		if span.open < tokenIndex && tokenIndex < span.close {
			containing = append(containing, span)
		}
	}
	slices.SortFunc(containing, func(a, b namespaceSpan) int { return a.open - b.open })
	parts := make([]string, 0, len(containing)+1)
	for _, span := range containing {
		parts = append(parts, span.name)
	}
	parts = append(parts, name)
	return strings.Join(parts, ".")
}

func extractEvents(sources []semanticSource) ([]EventRecord, error) {
	modules, err := buildSchemaModules(sources)
	if err != nil {
		return nil, err
	}
	definitions := make(map[string]*eventDefinition)
	for _, module := range modules {
		if err := discoverEvents(module, definitions); err != nil {
			return nil, err
		}
		discoverContainers(module)
	}
	manifestRoots := []struct {
		module string
		name   string
		label  string
	}{
		{module: "packages/schema/src/event-manifest.ts", name: "Definitions", label: "all"},
		{module: "packages/schema/src/event-manifest.ts", name: "ServerDefinitions", label: "server"},
		{module: "packages/schema/src/durable-event-manifest.ts", name: "Durable", label: "durable"},
		{module: "packages/schema/src/durable-event-manifest.ts", name: "SessionDurable.definitions", label: "session-durable"},
	}
	for _, root := range manifestRoots {
		members := resolveContainer(modules, definitions, root.module, root.name, make(map[string]bool))
		for id := range members {
			definition := definitions[id]
			if definition != nil && !slices.Contains(definition.record.Manifests, root.label) {
				definition.record.Manifests = append(definition.record.Manifests, root.label)
			}
		}
	}
	records := make([]EventRecord, 0, len(definitions))
	for _, definition := range definitions {
		slices.Sort(definition.record.Manifests)
		if definition.record.Manifests == nil {
			definition.record.Manifests = []string{}
		}
		records = append(records, definition.record)
	}
	return records, nil
}

func discoverEvents(module *tsModule, definitions map[string]*eventDefinition) error {
	spans := namespaceSpans(module.tokens)
	exposures := eventExposures(module)
	for index := 0; index < len(module.tokens); index++ {
		callStart, open, ok := eventDefineCall(module, index)
		if !ok {
			continue
		}
		close := matchToken(module.tokens, open, "(", ")")
		if close < 0 {
			return fmt.Errorf("unbalanced event definition in %s:%d", module.path, module.tokens[callStart].line)
		}
		localSymbol, exposedSymbol, found := assignedEventSymbol(module, spans, exposures, callStart)
		if !found {
			index = close
			continue
		}
		arguments := splitTopLevel(module.tokens[open+1:close], ",")
		if len(arguments) == 0 || len(arguments[0]) < 2 || arguments[0][0].raw != "{" {
			index = close
			continue
		}
		objectClose := matchToken(arguments[0], 0, "{", "}")
		if objectClose < 0 {
			return fmt.Errorf("unbalanced event object in %s:%d", module.path, module.tokens[callStart].line)
		}
		fields := objectProperties(arguments[0][1:objectClose])
		eventType, ok := evaluateStatic(fields["type"], module.source, module.static, make(map[string]bool))
		if !ok || eventType == "" {
			return fmt.Errorf("unresolved event type in %s:%d", module.path, module.tokens[callStart].line)
		}
		symbol := module.namespace + "." + exposedSymbol
		durable, aggregate, version := eventDurability(arguments[0][1:objectClose], module.static, make(map[string]bool))
		id := module.path + "|" + localSymbol + "|" + strconv.Itoa(version)
		definition := &eventDefinition{
			id: id, modulePath: module.path, localSymbol: localSymbol,
			record: EventRecord{
				Type: eventType, Symbol: symbol, SourcePath: module.path, Line: module.tokens[callStart].line,
				Classification: semanticScope(module.path), Durable: durable, DurableAggregate: aggregate,
				DurableVersion: version, Manifests: []string{},
			},
		}
		module.events = append(module.events, definition)
		definitions[id] = definition
		index = close
	}
	return nil
}

func eventDefineCall(module *tsModule, index int) (int, int, bool) {
	if index+3 < len(module.tokens) && module.tokens[index].raw == "Event" && module.tokens[index+1].raw == "." && module.tokens[index+2].raw == "define" && module.tokens[index+3].raw == "(" {
		return index, index + 3, true
	}
	if index+1 < len(module.tokens) && module.tokens[index].raw == "define" && module.tokens[index+1].raw == "(" {
		target := module.imports["define"]
		if strings.HasSuffix(target, "/event.ts") {
			return index, index + 1, true
		}
	}
	return 0, 0, false
}

func assignedConst(tokens []tsToken, callStart int) (string, int, bool) {
	if callStart < 3 || tokens[callStart-1].raw != "=" || tokens[callStart-2].kind != tsIdentifier || tokens[callStart-3].raw != "const" {
		return "", 0, false
	}
	exported := callStart >= 4 && tokens[callStart-4].raw == "export"
	return tokens[callStart-2].raw, callStart - 2, exported
}

func assignedEventSymbol(module *tsModule, spans []namespaceSpan, exposures map[string]string, callStart int) (string, string, bool) {
	if name, nameIndex, exported := assignedConst(module.tokens, callStart); name != "" {
		local := qualifiedAt(spans, nameIndex, name)
		if exported {
			return local, local, true
		}
		if exposed := exposures[name]; exposed != "" {
			return local, exposed, true
		}
		return local, local, true
	}
	if callStart < 2 || module.tokens[callStart-1].raw != ":" || module.tokens[callStart-2].kind != tsIdentifier {
		return "", "", false
	}
	root, rootIndex, exported := enclosingAssignedObject(module.tokens, callStart)
	if root == "" {
		return "", "", false
	}
	property := module.tokens[callStart-2].raw
	local := qualifiedAt(spans, rootIndex, root) + "." + property
	if exported {
		return local, local, true
	}
	if prefix := exposures[root+".*"]; prefix != "" {
		return local, strings.TrimSuffix(prefix, "*") + property, true
	}
	return local, local, true
}

func enclosingAssignedObject(tokens []tsToken, position int) (string, int, bool) {
	stack := make([]int, 0)
	for index := 0; index < position; index++ {
		switch tokens[index].raw {
		case "{":
			stack = append(stack, index)
		case "}":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	for cursor := len(stack) - 1; cursor >= 0; cursor-- {
		open := stack[cursor]
		if open < 3 || tokens[open-1].raw != "=" || tokens[open-2].kind != tsIdentifier || tokens[open-3].raw != "const" {
			continue
		}
		exported := open >= 4 && tokens[open-4].raw == "export"
		return tokens[open-2].raw, open - 2, exported
	}
	return "", 0, false
}

func eventExposures(module *tsModule) map[string]string {
	result := make(map[string]string)
	for index := 0; index+4 < len(module.tokens); index++ {
		if module.tokens[index].raw != "export" || module.tokens[index+1].raw != "const" || module.tokens[index+2].kind != tsIdentifier || module.tokens[index+3].raw != "=" || module.tokens[index+4].raw != "{" {
			continue
		}
		close := matchToken(module.tokens, index+4, "{", "}")
		if close < 0 {
			continue
		}
		root := module.tokens[index+2].raw
		for _, field := range splitTopLevel(module.tokens[index+5:close], ",") {
			if len(field) == 2 && field[0].raw == "..." && field[1].kind == tsIdentifier {
				result[field[1].raw+".*"] = root + ".*"
				continue
			}
			if len(field) == 1 && field[0].kind == tsIdentifier {
				result[field[0].raw] = root + "." + field[0].raw
				continue
			}
			colon := topLevelToken(field, ":")
			if colon == 1 && field[0].kind == tsIdentifier {
				if value, ok := dottedName(field[colon+1:]); ok {
					parts := strings.Split(value, ".")
					result[parts[len(parts)-1]] = root + "." + field[0].raw
				}
			}
		}
		index = close
	}
	return result
}

func eventDurability(object []tsToken, environment map[string]tsExpression, visiting map[string]bool) (bool, string, int) {
	fields := objectProperties(object)
	if durableTokens, exists := fields["durable"]; exists {
		durableTokens = trimTokens(durableTokens)
		if len(durableTokens) > 1 && durableTokens[0].raw == "{" {
			close := matchToken(durableTokens, 0, "{", "}")
			if close > 0 {
				durableFields := objectProperties(durableTokens[1:close])
				aggregate, _ := evaluateStatic(durableFields["aggregate"], nil, environment, make(map[string]bool))
				version := 0
				if values := durableFields["version"]; len(values) == 1 && values[0].kind == tsNumber {
					version, _ = strconv.Atoi(values[0].raw)
				}
				return true, aggregate, version
			}
		}
	}
	for _, field := range splitTopLevel(object, ",") {
		if len(field) < 2 || field[0].raw != "..." {
			continue
		}
		name, ok := dottedName(field[1:])
		if !ok || visiting[name] {
			continue
		}
		expression, exists := environment[name]
		if !exists {
			continue
		}
		visiting[name] = true
		value := trimTokens(expression.tokens)
		if len(value) > 1 && value[0].raw == "{" {
			close := matchToken(value, 0, "{", "}")
			if close > 0 {
				if durable, aggregate, version := eventDurability(value[1:close], environment, visiting); durable {
					delete(visiting, name)
					return durable, aggregate, version
				}
			}
		}
		delete(visiting, name)
	}
	return false, "", 0
}

func discoverContainers(module *tsModule) {
	spans := namespaceSpans(module.tokens)
	for index := 0; index < len(module.tokens); index++ {
		callStart, open, kind, ok := containerCall(module, index)
		if !ok {
			continue
		}
		close := matchToken(module.tokens, open, "(", ")")
		if close < 0 {
			continue
		}
		name := containerAssignedName(module.tokens, callStart, spans)
		if name == "" {
			index = close
			continue
		}
		arguments := module.tokens[open+1 : close]
		refs := parseContainerRefs(arguments, kind)
		module.containers[name] = eventContainer{modulePath: module.path, name: name, refs: refs}
		index = close
	}
	discoverFilteredContainerAliases(module, spans)
}

func discoverFilteredContainerAliases(module *tsModule, spans []namespaceSpan) {
	for index := 0; index+8 < len(module.tokens); index++ {
		if module.tokens[index].raw != "const" || module.tokens[index+1].kind != tsIdentifier || module.tokens[index+2].raw != "=" {
			continue
		}
		filter := -1
		for cursor := index + 3; cursor+2 < len(module.tokens) && module.tokens[cursor].line <= module.tokens[index].line+5; cursor++ {
			if module.tokens[cursor].raw == "." && module.tokens[cursor+1].raw == "filter" && module.tokens[cursor+2].raw == "(" {
				filter = cursor
				break
			}
		}
		if filter < 0 {
			continue
		}
		name, ok := dottedName(module.tokens[index+3 : filter])
		if !ok {
			continue
		}
		close := matchToken(module.tokens, filter+2, "(", ")")
		if close < 0 {
			continue
		}
		condition := strings.Join(tokenRaws(module.tokens[filter+3:close]), "")
		selection := 0
		if strings.Contains(condition, "!==undefined") {
			selection = 1
		}
		if strings.Contains(condition, "===undefined") {
			selection = -1
		}
		containerName := qualifiedAt(spans, index+1, module.tokens[index+1].raw)
		module.containers[containerName] = eventContainer{
			modulePath: module.path,
			name:       containerName,
			refs:       []containerRef{{name: name, filter: selection}},
		}
		index = close
	}
}

func containerCall(module *tsModule, index int) (int, int, string, bool) {
	if index+3 < len(module.tokens) && module.tokens[index].raw == "Event" && module.tokens[index+1].raw == "." && (module.tokens[index+2].raw == "inventory" || module.tokens[index+2].raw == "durable") && module.tokens[index+3].raw == "(" {
		return index, index + 3, module.tokens[index+2].raw, true
	}
	if index+1 < len(module.tokens) && (module.tokens[index].raw == "inventory" || module.tokens[index].raw == "durable") && module.tokens[index+1].raw == "(" {
		if target := module.imports[module.tokens[index].raw]; strings.HasSuffix(target, "/event.ts") {
			return index, index + 1, module.tokens[index].raw, true
		}
	}
	return 0, 0, "", false
}

func containerAssignedName(tokens []tsToken, callStart int, spans []namespaceSpan) string {
	if name, nameIndex, _ := assignedConst(tokens, callStart); name != "" {
		return qualifiedAt(spans, nameIndex, name)
	}
	if callStart < 2 || tokens[callStart-1].raw != ":" || tokens[callStart-2].kind != tsIdentifier {
		return ""
	}
	property := tokens[callStart-2].raw
	stack := make([]int, 0)
	for index := 0; index < callStart; index++ {
		switch tokens[index].raw {
		case "{":
			stack = append(stack, index)
		case "}":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	for cursor := len(stack) - 1; cursor >= 0; cursor-- {
		open := stack[cursor]
		if open >= 3 && tokens[open-1].raw == "=" && tokens[open-2].kind == tsIdentifier && tokens[open-3].raw == "const" {
			root := qualifiedAt(spans, open-2, tokens[open-2].raw)
			return root + "." + property
		}
	}
	return ""
}

func parseContainerRefs(arguments []tsToken, kind string) []containerRef {
	arguments = trimTokens(arguments)
	if kind == "durable" && len(arguments) > 1 && arguments[0].raw == "[" {
		if close := matchToken(arguments, 0, "[", "]"); close > 0 {
			arguments = arguments[1:close]
		}
	}
	refs := make([]containerRef, 0)
	for _, argument := range splitTopLevel(arguments, ",") {
		argument = trimTokens(argument)
		if len(argument) == 0 {
			continue
		}
		if argument[0].raw == "..." {
			argument = argument[1:]
		}
		filter := 0
		filterIndex := -1
		for index := 0; index+1 < len(argument); index++ {
			if argument[index].raw == "." && argument[index+1].raw == "filter" {
				filterIndex = index
				break
			}
		}
		if filterIndex >= 0 {
			filterRaw := strings.Join(tokenRaws(argument[filterIndex:]), "")
			if strings.Contains(filterRaw, "!==undefined") {
				filter = 1
			}
			if strings.Contains(filterRaw, "===undefined") {
				filter = -1
			}
			argument = argument[:filterIndex]
		}
		name, ok := dottedName(argument)
		if ok {
			refs = append(refs, containerRef{name: name, filter: filter})
		}
	}
	return refs
}

func tokenRaws(tokens []tsToken) []string {
	result := make([]string, len(tokens))
	for index, token := range tokens {
		result[index] = token.raw
	}
	return result
}

func resolveContainer(modules map[string]*tsModule, definitions map[string]*eventDefinition, modulePath string, name string, visiting map[string]bool) map[string]struct{} {
	key := modulePath + "|" + name
	if visiting[key] {
		return map[string]struct{}{}
	}
	visiting[key] = true
	defer delete(visiting, key)
	module := modules[modulePath]
	if module == nil {
		return map[string]struct{}{}
	}
	container, exists := module.containers[name]
	if !exists && module.reexportTarget != "" {
		return resolveContainer(modules, definitions, module.reexportTarget, name, visiting)
	}
	if !exists {
		return map[string]struct{}{}
	}
	result := make(map[string]struct{})
	for _, ref := range container.refs {
		members := resolveEventReference(modules, definitions, module, ref.name, visiting)
		for id := range members {
			definition := definitions[id]
			if definition == nil || ref.filter == 1 && !definition.record.Durable || ref.filter == -1 && definition.record.Durable {
				continue
			}
			result[id] = struct{}{}
		}
	}
	return result
}

func resolveEventReference(modules map[string]*tsModule, definitions map[string]*eventDefinition, module *tsModule, name string, visiting map[string]bool) map[string]struct{} {
	targetModule := module
	targetName := name
	parts := strings.Split(name, ".")
	if imported := module.imports[parts[0]]; imported != "" {
		targetModule = modules[imported]
		targetName = strings.Join(parts[1:], ".")
	}
	if targetModule == nil || targetName == "" {
		return map[string]struct{}{}
	}
	for _, event := range targetModule.events {
		if event.localSymbol == targetName || strings.HasSuffix(event.record.Symbol, "."+targetName) {
			return map[string]struct{}{event.id: {}}
		}
	}
	if _, exists := targetModule.containers[targetName]; exists {
		return resolveContainer(modules, definitions, targetModule.path, targetName, visiting)
	}
	if targetModule.reexportTarget != "" {
		facade := modules[targetModule.reexportTarget]
		if facade != nil {
			return resolveEventReference(modules, definitions, facade, targetName, visiting)
		}
	}
	return map[string]struct{}{}
}
