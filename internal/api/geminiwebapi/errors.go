package geminiwebapi

import "fmt"

type AuthError struct{ Msg string }
func (e *AuthError) Error() string { if e.Msg=="" { return "authentication error" } ; return e.Msg }

type APIError struct{ Msg string }
func (e *APIError) Error() string { if e.Msg=="" { return "api error" } ; return e.Msg }

type ImageGenerationError struct{ APIError }

type GeminiError struct{ Msg string }
func (e *GeminiError) Error() string { if e.Msg=="" { return "gemini error" } ; return e.Msg }

type TimeoutError struct{ GeminiError }

type UsageLimitExceeded struct{ GeminiError }

type ModelInvalid struct{ GeminiError }

type TemporarilyBlocked struct{ GeminiError }

type ValueError struct{ Msg string }
func (e *ValueError) Error() string { if e.Msg=="" { return "value error" } ; return e.Msg }

func Wrapf(err error, format string, a ...any) error {
    return fmt.Errorf(format+": %w", append(a, err)...)
}

