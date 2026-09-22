package raildrop

import (
	"errors"
	"fmt"
	"net/http"
)

type ErrorCode string

const (
	CodeBadRequest    ErrorCode = "BAD_REQUEST"
	CodeForbidden     ErrorCode = "FORBIDDEN"
	CodeNotFound      ErrorCode = "NOT_FOUND"
	CodeTooLarge      ErrorCode = "TOO_LARGE"
	CodeInvalidType   ErrorCode = "INVALID_TYPE"
	CodeExpired       ErrorCode = "EXPIRED"
	CodeStorageError  ErrorCode = "STORAGE_ERROR"
	CodeCallbackError ErrorCode = "CALLBACK_ERROR"
)

type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("raildrop: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("raildrop: %s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WrapError(code ErrorCode, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

func HTTPStatus(code ErrorCode) int {
	switch code {
	case CodeForbidden, CodeExpired:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeStorageError, CodeCallbackError:
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

func AsError(err error) (*Error, bool) {
	var raildropErr *Error
	if errors.As(err, &raildropErr) {
		return raildropErr, true
	}
	return nil, false
}
