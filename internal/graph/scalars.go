package graph

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// UUID is a graph-gophers-friendly wrapper around uuid.UUID. It
// implements UnmarshalGraphQL/MarshalJSON so the schema scalar `UUID`
// resolves to a real Go type instead of a free-form string.
type UUID struct {
	V uuid.UUID
}

// ImplementsGraphQLType is the marker graph-gophers looks for.
func (UUID) ImplementsGraphQLType(name string) bool { return name == "UUID" }

// UnmarshalGraphQL parses an incoming variable.
func (u *UUID) UnmarshalGraphQL(input any) error {
	s, ok := input.(string)
	if !ok {
		return fmt.Errorf("UUID: expected string, got %T", input)
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return fmt.Errorf("UUID: %w", err)
	}
	u.V = id
	return nil
}

// MarshalJSON renders the value back as a string.
func (u UUID) MarshalJSON() ([]byte, error) {
	return json.Marshal(u.V.String())
}

// DateTime is a graph-gophers-friendly RFC 3339 wrapper.
type DateTime struct {
	T time.Time
}

func (DateTime) ImplementsGraphQLType(name string) bool { return name == "DateTime" }

func (d *DateTime) UnmarshalGraphQL(input any) error {
	s, ok := input.(string)
	if !ok {
		return fmt.Errorf("DateTime: expected string, got %T", input)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return fmt.Errorf("DateTime: %w", err)
		}
	}
	d.T = t.UTC()
	return nil
}

func (d DateTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.T.UTC().Format(time.RFC3339Nano))
}
