package dnsx

import "errors"

// errorsAs is a thin wrapper around errors.As so verify.go doesn't need
// its own errors import (avoids a small import churn in that file).
func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
