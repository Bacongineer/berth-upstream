//go:build !linux

package claims

import "errors"

// Scan is Linux-only in this release; the Windows process table comes with
// the Windows node.
func Scan() ([]Proc, error) { return nil, errors.ErrUnsupported }
