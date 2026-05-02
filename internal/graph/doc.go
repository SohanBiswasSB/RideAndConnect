// Package graph holds gqlgen-generated code (under internal/graph/generated)
// and hand-written resolvers + scalar implementations that live alongside
// it.
//
// Source of truth: schema/*.graphqls. Run `make graphql-gen` to regenerate
// after schema changes. Resolver bodies are preserved across regenerations
// per gqlgen.yml (preserve_template_invocations: true).
//
// Resolvers are thin: validate inputs (via internal/validation), call
// domain services (internal/auth, internal/rides, internal/destination,
// internal/routes), and map domain errors to GraphQL extensions.code
// values listed in docs/API.md §1.3.
package graph
