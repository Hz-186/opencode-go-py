package domain

import "fmt"

type ProviderMetadata map[string]map[string]JSONValue

func (metadata ProviderMetadata) Validate() error {
	for provider, values := range metadata {
		for key, value := range values {
			if err := value.Validate(); err != nil {
				return fmt.Errorf("invalid provider metadata %s.%s: %w", provider, key, err)
			}
		}
	}
	return nil
}
