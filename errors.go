package agentping

import "errors"

// ErrNotInitialized is returned when SDK calls happen before Init.
var ErrNotInitialized = errors.New("agentping: Init has not been called")

// ErrInvalidAPIKey is returned when the supplied API key fails format checks.
var ErrInvalidAPIKey = errors.New("agentping: invalid API key format")
