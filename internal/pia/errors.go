package pia

import "fmt"

// AuthError indicates a fatal authentication failure, such as invalid or
// expired credentials. The operation should not be retried without user
// intervention (e.g. correcting PIA_USER / PIA_PASS).
type AuthError struct {
	Msg string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("authentication error: %s", e.Msg)
}

// ConnectivityError indicates a transient network failure, such as DNS
// resolution timeouts, unreachable servers, or HTTP connection errors.
// The caller should verify WAN connectivity and retry.
type ConnectivityError struct {
	Op  string // operation that failed, e.g. "dns", "auth", "serverlist", "addkey"
	Msg string
	Err error // underlying error, if any
}

func (e *ConnectivityError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Msg)
}

func (e *ConnectivityError) Unwrap() error {
	return e.Err
}

type LocationError struct {
	Msg      string
	Location string
}

func (e *LocationError) Error() string {
	return fmt.Sprintf("%s %s", e.Location, e.Msg)
}

type TokenRejectedError struct {
	Msg string
}

func (e *TokenRejectedError) Error() string {
	return fmt.Sprintf("Token rejected: %s", e.Msg)
}
