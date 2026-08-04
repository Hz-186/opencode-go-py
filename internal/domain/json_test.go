package domain

import "testing"

func TestJSONValueConstructorsPreserveKinds(t *testing.T) {
	value := JSONObject(map[string]JSONValue{
		"null":   JSONNull(),
		"bool":   JSONBool(true),
		"number": JSONNumber("9007199254740993"),
		"string": JSONString("你好"),
		"array":  JSONArray([]JSONValue{JSONString("item")}),
	})
	if value.Kind != JSONKindObject || value.Object["number"].Number != "9007199254740993" {
		t.Fatalf("JSON value = %#v", value)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("validate JSON value: %v", err)
	}
	if err := (JSONValue{}).Validate(); err == nil {
		t.Fatal("zero JSON value unexpectedly validated")
	}
}
