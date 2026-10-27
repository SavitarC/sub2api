package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const ClineUpstreamPinsCredentialKey = "cline_upstream_pins"

type ClineUpstreamPin struct {
	Pipeline string   `json:"pipeline"`
	Upstream string   `json:"upstream,omitempty"`
	Has      []string `json:"has,omitempty"`
}

var clineProviderIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)

func NormalizeClineUpstreamPin(pin ClineUpstreamPin) (ClineUpstreamPin, error) {
	pin.Pipeline = strings.ToLower(strings.TrimSpace(pin.Pipeline))
	if pin.Pipeline == "" {
		pin.Pipeline = "unknown"
	}
	switch pin.Pipeline {
	case "planner", "direct", "unknown":
	default:
		return pin, invalidClineUpstreamPin("pipeline must be planner, direct or unknown")
	}
	pin.Upstream = strings.TrimSpace(pin.Upstream)
	if pin.Upstream != "" && !clineProviderIDPattern.MatchString(pin.Upstream) {
		return pin, invalidClineUpstreamPin("invalid upstream provider ID")
	}
	if pin.Pipeline == "direct" {
		pin.Has = nil
		return pin, nil
	}
	has := make([]string, 0, 2)
	for _, capability := range pin.Has {
		switch capability {
		case "vision", "implicit-caching":
		default:
			return pin, invalidClineUpstreamPin("has must contain only vision or implicit-caching")
		}
		if !slices.Contains(has, capability) {
			has = append(has, capability)
		}
	}
	pin.Has = has
	return pin, nil
}

func invalidClineUpstreamPin(message string) error {
	return infraerrors.BadRequest("INVALID_CLINE_UPSTREAM_PIN", message)
}

func parseClineUpstreamPins(raw any) (map[string]ClineUpstreamPin, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, invalidClineUpstreamPin("invalid cline_upstream_pins configuration")
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &entries); err != nil || entries == nil {
		return nil, invalidClineUpstreamPin("cline_upstream_pins must be an object keyed by model ID")
	}
	if len(entries) > 100 {
		return nil, invalidClineUpstreamPin("cline_upstream_pins supports at most 100 models")
	}
	pins := make(map[string]ClineUpstreamPin, len(entries))
	for model, value := range entries {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 256 || strings.Contains(model, "*") || strings.IndexFunc(model, unicode.IsSpace) >= 0 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
			return nil, invalidClineUpstreamPin("pins require a concrete model ID without whitespace")
		}
		if _, duplicate := pins[model]; duplicate {
			return nil, invalidClineUpstreamPin("duplicate model ID in cline_upstream_pins")
		}
		if !gjson.ParseBytes(value).IsObject() {
			return nil, invalidClineUpstreamPin("each model pin must be an object")
		}
		var pin ClineUpstreamPin
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&pin); err != nil {
			return nil, invalidClineUpstreamPin("invalid model pin fields")
		}
		pin, err = NormalizeClineUpstreamPin(pin)
		if err != nil {
			return nil, err
		}
		pins[model] = pin
	}
	return pins, nil
}

func NormalizeClineUpstreamPinCredentials(credentials map[string]any) error {
	raw, exists := credentials[ClineUpstreamPinsCredentialKey]
	if !exists {
		return nil
	}
	if raw == nil {
		return nil
	}
	pins, err := parseClineUpstreamPins(raw)
	if err != nil {
		return err
	}
	credentials[ClineUpstreamPinsCredentialKey] = pins
	return nil
}

// ApplyClineUpstreamPin uses the final model ID, after protocol conversion and mapping.
func ApplyClineUpstreamPin(account *Account, body []byte) ([]byte, error) {
	if !account.IsCline() || account.Credentials[ClineUpstreamPinsCredentialKey] == nil {
		return body, nil
	}
	pins, err := parseClineUpstreamPins(account.Credentials[ClineUpstreamPinsCredentialKey])
	if err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return body, nil
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, invalidClineUpstreamPin("invalid request body")
	}
	// JSON routers disagree on duplicate keys; do not let a later key bypass a pin.
	if _, err := clineRoutingObject(body, ""); err != nil {
		return nil, err
	}
	pin, exists := pins[gjson.GetBytes(body, "model").String()]
	if !exists {
		return body, nil
	}
	if pin.Pipeline == "direct" {
		if _, err := clineRoutingObject(body, "providerOptions"); err != nil {
			return nil, err
		}
		if _, err := clineRoutingObject(body, "providerOptions.gateway"); err != nil {
			return nil, err
		}
		body, err = sjson.DeleteBytes(body, "providerOptions.gateway.has")
		if err != nil {
			return nil, err
		}
	}
	if pin.Upstream == "" && len(pin.Has) == 0 {
		return body, nil
	}
	if pin.Pipeline != "direct" {
		if _, err := clineRoutingObject(body, "providerOptions"); err != nil {
			return nil, err
		}
		gateway, err := clineRoutingObject(body, "providerOptions.gateway")
		if err != nil {
			return nil, err
		}
		if pin.Upstream != "" {
			gateway["only"], _ = json.Marshal([]string{pin.Upstream})
			delete(gateway, "order")
		}
		if len(pin.Has) > 0 {
			has := []string{}
			if existing, ok := gateway["has"]; ok && string(existing) != "null" {
				if err := json.Unmarshal(existing, &has); err != nil {
					return nil, invalidClineUpstreamPin("providerOptions.gateway.has must be an array of strings")
				}
			}
			for _, capability := range pin.Has {
				if !slices.Contains(has, capability) {
					has = append(has, capability)
				}
			}
			gateway["has"], _ = json.Marshal(has)
		}
		body, err = sjson.SetBytes(body, "providerOptions.gateway", gateway)
		if err != nil {
			return nil, err
		}
	}
	if pin.Pipeline != "planner" && pin.Upstream != "" {
		provider, err := clineRoutingObject(body, "provider")
		if err != nil {
			return nil, err
		}
		provider["only"], _ = json.Marshal([]string{pin.Upstream})
		provider["allow_fallbacks"] = json.RawMessage("false")
		delete(provider, "order")
		delete(provider, "ignore")
		body, err = sjson.SetBytes(body, "provider", provider)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func clineRoutingObject(body []byte, path string) (map[string]json.RawMessage, error) {
	value := gjson.ParseBytes(body)
	if path != "" {
		value = value.Get(path)
	}
	result := make(map[string]json.RawMessage)
	if !value.Exists() || value.Type == gjson.Null {
		return result, nil
	}
	if !value.IsObject() {
		return nil, invalidClineUpstreamPin(path + " must be an object")
	}
	var err error
	value.ForEach(func(key, value gjson.Result) bool {
		if _, duplicate := result[key.String()]; duplicate {
			err = invalidClineUpstreamPin("duplicate request or routing field")
			return false
		}
		result[key.String()] = json.RawMessage(value.Raw)
		return true
	})
	return result, err
}

// Protocol converters do not retain provider extensions in their typed request.
func preserveClineRoutingOptions(account *Account, converted, original []byte) ([]byte, error) {
	if !account.IsCline() {
		return converted, nil
	}
	for _, field := range []string{"provider", "providerOptions"} {
		if value := gjson.GetBytes(original, field); value.Exists() {
			var err error
			converted, err = sjson.SetRawBytes(converted, field, []byte(value.Raw))
			if err != nil {
				return nil, fmt.Errorf("preserve Cline routing options: %w", err)
			}
		}
	}
	return converted, nil
}
