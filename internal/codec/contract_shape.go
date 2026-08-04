package codec

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

type contractRule func(domain.JSONValue) error

type contractFieldRule struct {
	rule     contractRule
	optional bool
}

func requiredShape(rule contractRule) contractFieldRule {
	return contractFieldRule{rule: rule}
}

func optionalShape(rule contractRule) contractFieldRule {
	return contractFieldRule{rule: rule, optional: true}
}

func contractObjectShape(label string, fields map[string]contractFieldRule) contractRule {
	return func(value domain.JSONValue) error {
		if value.Kind != domain.JSONKindObject {
			return fmt.Errorf("%s must be an object", label)
		}
		for field := range value.Object {
			if _, known := fields[field]; !known {
				return fmt.Errorf("unknown %s field %q", label, field)
			}
		}
		for field, fieldRule := range fields {
			item, present := value.Object[field]
			if !present {
				if fieldRule.optional {
					continue
				}
				return fmt.Errorf("%s field %q is required", label, field)
			}
			if err := fieldRule.rule(item); err != nil {
				return fmt.Errorf("invalid %s field %q: %w", label, field, err)
			}
		}
		return nil
	}
}

func contractStringShape(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindString {
		return errors.New("must be a string")
	}
	return nil
}

func contractBoolShape(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindBool {
		return errors.New("must be a boolean")
	}
	return nil
}

func contractFiniteNumberShape(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindNumber {
		return errors.New("must be a number")
	}
	number, err := strconv.ParseFloat(value.Number, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return errors.New("must be finite")
	}
	return nil
}

func contractNonNegativeIntShape(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindNumber {
		return errors.New("must be an integer")
	}
	number, _, err := big.ParseFloat(value.Number, 10, 256, big.ToNearestEven)
	if err != nil {
		return errors.New("must be an integer")
	}
	integer, accuracy := number.Int(nil)
	if accuracy != big.Exact || integer.Sign() < 0 {
		return errors.New("must be a non-negative integer")
	}
	return nil
}

func contractLiteralShape(values ...string) contractRule {
	return func(value domain.JSONValue) error {
		if err := contractStringShape(value); err != nil {
			return err
		}
		for _, allowed := range values {
			if value.String == allowed {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v", values)
	}
}

func contractArrayShape(label string, itemRule contractRule) contractRule {
	return func(value domain.JSONValue) error {
		if value.Kind != domain.JSONKindArray {
			return fmt.Errorf("%s must be an array", label)
		}
		for index, item := range value.Array {
			if err := itemRule(item); err != nil {
				return fmt.Errorf("invalid %s item %d: %w", label, index, err)
			}
		}
		return nil
	}
}

func contractRecordShape(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindObject {
		return errors.New("must be an object")
	}
	return nil
}

func contractStringRecordShape(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindObject {
		return errors.New("must be an object")
	}
	for key, item := range value.Object {
		if item.Kind != domain.JSONKindString {
			return fmt.Errorf("field %q must be a string", key)
		}
	}
	return nil
}

func contractNonNullUnknownShape(value domain.JSONValue) error {
	if value.Kind == domain.JSONKindNull {
		return errors.New("must not be null")
	}
	return nil
}

func contractProviderMetadataShape(value domain.JSONValue) error {
	_, err := providerMetadataFromJSONValue(value)
	return err
}

func contractEventIDShape(value domain.JSONValue) error {
	if err := contractStringShape(value); err != nil {
		return err
	}
	_, err := domain.ParseEventID(value.String)
	return err
}

func contractMessageIDShape(value domain.JSONValue) error {
	if err := contractStringShape(value); err != nil {
		return err
	}
	_, err := domain.ParseMessageID(value.String)
	return err
}

func contractSessionIDShape(value domain.JSONValue) error {
	if err := contractStringShape(value); err != nil {
		return err
	}
	_, err := domain.ParseSessionID(value.String)
	return err
}

func contractWorkspaceIDShape(value domain.JSONValue) error {
	if err := contractStringShape(value); err != nil {
		return err
	}
	_, err := domain.ParseWorkspaceID(value.String)
	return err
}
