// Package config loads process configuration from environment variables into
// typed Go structs.
package config

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	durationType     = reflect.TypeFor[time.Duration]()
	textUnmarshaler  = reflect.TypeFor[encoding.TextUnmarshaler]()
	errInvalidTarget = errors.New("target must be a non-nil pointer to a struct")
)

// LookupFunc retrieves an environment variable. Its signature matches
// os.LookupEnv, which makes configuration loading straightforward to test.
type LookupFunc func(key string) (value string, found bool)

// Validator may be implemented by a configuration struct to enforce
// constraints which cannot be expressed by environment tags.
type Validator interface {
	Validate() error
}

// FieldError describes an invalid or missing environment variable.
type FieldError struct {
	Variable string
	Err      error
}

func (e FieldError) Error() string {
	return fmt.Sprintf("%s: %v", e.Variable, e.Err)
}

func (e FieldError) Unwrap() error {
	return e.Err
}

// Error contains all field-level configuration problems found while loading.
// Reporting all problems at once avoids repeated edit-and-restart cycles.
type Error struct {
	Fields []FieldError
}

func (e *Error) Error() string {
	problems := make([]string, len(e.Fields))
	for i, field := range e.Fields {
		problems[i] = field.Error()
	}

	return "invalid configuration: " + strings.Join(problems, "; ")
}

// Load populates target from the process environment. Target must be a
// non-nil pointer to a struct. Only exported fields carrying an env tag are
// considered.
//
// Supported tags are:
//
//	`env:"PORT" default:"8080"`
//	`env:"DATABASE_URL" required:"true"`
//
// Supported values include strings, booleans, integers, unsigned integers,
// floating-point numbers, time.Duration, and types implementing
// encoding.TextUnmarshaler. Target is changed only when loading and validation
// both succeed.
func Load(target any) error {
	return LoadFrom(target, os.LookupEnv)
}

// LoadFrom behaves like Load but gets values through lookup. It is useful for
// deterministic tests and for callers backed by an environment-compatible
// source.
func LoadFrom(target any, lookup LookupFunc) error {
	if lookup == nil {
		return errors.New("config: lookup function must not be nil")
	}

	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Pointer || targetValue.IsNil() || targetValue.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: %w", errInvalidTarget)
	}

	// Decode into a copy so a failed load never leaves partially applied config.
	decoded := reflect.New(targetValue.Elem().Type())
	decoded.Elem().Set(targetValue.Elem())

	var fieldErrors []FieldError
	structType := decoded.Elem().Type()
	for i := range structType.NumField() {
		fieldType := structType.Field(i)
		variable, configured := fieldType.Tag.Lookup("env")
		if !configured || variable == "-" {
			continue
		}

		if variable == "" {
			fieldErrors = append(fieldErrors, FieldError{
				Variable: fieldType.Name,
				Err:      errors.New("env tag must not be empty"),
			})
			continue
		}

		fieldValue := decoded.Elem().Field(i)
		if !fieldValue.CanSet() {
			fieldErrors = append(fieldErrors, FieldError{
				Variable: variable,
				Err:      errors.New("field must be exported"),
			})
			continue
		}

		required, err := requiredField(fieldType)
		if err != nil {
			fieldErrors = append(fieldErrors, FieldError{Variable: variable, Err: err})
			continue
		}

		raw, found := lookup(variable)
		if !found {
			raw, found = fieldType.Tag.Lookup("default")
		}

		if !found {
			if required {
				fieldErrors = append(fieldErrors, FieldError{
					Variable: variable,
					Err:      errors.New("required environment variable is not set"),
				})
			}
			continue
		}

		if required && strings.TrimSpace(raw) == "" {
			fieldErrors = append(fieldErrors, FieldError{
				Variable: variable,
				Err:      errors.New("required environment variable must not be empty"),
			})
			continue
		}

		if err := parseValue(fieldValue, raw); err != nil {
			fieldErrors = append(fieldErrors, FieldError{Variable: variable, Err: err})
		}
	}

	if len(fieldErrors) > 0 {
		return &Error{Fields: fieldErrors}
	}

	if validator, ok := decoded.Interface().(Validator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("invalid configuration: validation failed: %w", err)
		}
	}

	targetValue.Elem().Set(decoded.Elem())
	return nil
}

func requiredField(field reflect.StructField) (bool, error) {
	raw, exists := field.Tag.Lookup("required")
	if !exists {
		return false, nil
	}

	required, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("required tag must be true or false, got %q", raw)
	}

	return required, nil
}

func parseValue(field reflect.Value, raw string) error {
	if field.CanAddr() && field.Addr().Type().Implements(textUnmarshaler) {
		unmarshaler := field.Addr().Interface().(encoding.TextUnmarshaler)
		if err := unmarshaler.UnmarshalText([]byte(raw)); err != nil {
			return fmt.Errorf("cannot parse value: %w", err)
		}
		return nil
	}

	if field.Type() == durationType {
		value, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("must be a duration such as 500ms or 2m: %w", err)
		}
		field.SetInt(int64(value))
		return nil
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("must be a boolean: %w", err)
		}
		field.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(raw, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("must be an integer: %w", err)
		}
		field.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(raw, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("must be an unsigned integer: %w", err)
		}
		field.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(raw, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("must be a number: %w", err)
		}
		field.SetFloat(value)
	default:
		return fmt.Errorf("unsupported field type %s", field.Type())
	}

	return nil
}
