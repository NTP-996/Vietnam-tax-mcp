// Package httpx is the JSON response layer: the error envelope
// {error: {code, message, details?}} with its stable codes, and
// the small helpers every handler uses.
package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// The stable error codes. Clients switch on the code, never on
// the message.
const (
	CodeUnauthorized          = "unauthorized"
	CodeForbiddenEntity       = "forbidden_entity"
	CodeForbiddenRole         = "forbidden_role"
	CodeEntityExists          = "entity_exists"
	CodeUserExists            = "user_exists"
	CodeMembershipRevoked     = "membership_revoked"
	CodeProposalExpired       = "proposal_expired"
	CodeProposalNotProposed   = "proposal_not_proposed"
	CodeEntryUnbalanced       = "entry_unbalanced"
	CodePeriodLocked          = "period_locked"
	CodeGatewayUnavailable    = "gateway_unavailable"
	CodeBudgetExhausted       = "budget_exhausted"
	CodeRateLimited           = "rate_limited"
	CodeValidationFailed      = "validation_failed"
	CodeBadLogin              = "invalid_credentials" // wrong password or one-time code
	CodeInvalidToken          = "invalid_token"
	CodeTOTPRequired          = "totp_required"
	CodeTOTPEnrolled          = "totp_enrolled"
	CodeInvitationInvalid     = "invitation_invalid"
	CodeNotFound              = "not_found"
	CodeReadOnly              = "read_only"
	CodeIdempotencyInProgress = "idempotency_in_progress"
	CodeInternal              = "internal_error"
)

// ErrorBody is the envelope.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one API error.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// OK writes a 200.
func OK(c *gin.Context, payload any) { c.JSON(http.StatusOK, payload) }

// Created writes a 201.
func Created(c *gin.Context, payload any) { c.JSON(http.StatusCreated, payload) }

// NoContent writes a 204.
func NoContent(c *gin.Context) { c.Status(http.StatusNoContent) }

// Error writes the envelope and aborts the handler chain.
func Error(c *gin.Context, status int, code, message string, details ...any) {
	body := ErrorBody{Error: ErrorDetail{Code: code, Message: message}}
	if len(details) > 0 {
		body.Error.Details = details[0]
	}
	c.AbortWithStatusJSON(status, body)
}

// Validation is a 400 validation_failed.
func Validation(c *gin.Context, message string, details ...any) {
	Error(c, http.StatusBadRequest, CodeValidationFailed, message, details...)
}

// Unauthorized is a 401.
func Unauthorized(c *gin.Context, code, message string) {
	Error(c, http.StatusUnauthorized, code, message)
}

// Forbidden is a 403.
func Forbidden(c *gin.Context, code, message string) {
	Error(c, http.StatusForbidden, code, message)
}

// NotFound is a 404.
func NotFound(c *gin.Context, message string) {
	Error(c, http.StatusNotFound, CodeNotFound, message)
}

// Internal is a 500 with no internal detail leaked.
func Internal(c *gin.Context) {
	Error(c, http.StatusInternalServerError, CodeInternal, "something went wrong")
}

// BindJSON decodes the body into dst and answers 400 validation_failed when it
// is not valid JSON; the caller returns when it reports false.
func BindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		Validation(c, "invalid JSON body", err.Error())
		return false
	}
	return true
}

// ValidationError is a user-facing 400 raised by a service for bad input,
// distinct from an unexpected internal error.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// Invalid wraps msg as a ValidationError.
func Invalid(msg string) error { return ValidationError{Msg: msg} }

// Mapping pairs a sentinel error with the response to write when it matches.
type Mapping struct {
	Err     error
	Status  int
	Code    string
	Message string
}

// RespondMapped is the shared shape behind every module's writeErr: a
// ValidationError anywhere in the chain wins (400); mappings are then tried in
// order with errors.Is; anything else is logged with the module prefix and
// answered with a generic 500.
func RespondMapped(c *gin.Context, err error, mappings []Mapping, module string) {
	var ve ValidationError
	if errors.As(err, &ve) {
		Validation(c, ve.Error())
		return
	}
	for _, m := range mappings {
		if errors.Is(err, m.Err) {
			Error(c, m.Status, m.Code, m.Message)
			return
		}
	}
	slog.Error(module+": unexpected error", "err", err, "request_id", c.GetString("request_id"))
	Internal(c)
}
