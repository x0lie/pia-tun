package apperrors

import (
	"errors"
)

var (
	ErrReconnect = errors.New("reconnect requested")
	ErrFatal     = errors.New("fatal")
)
