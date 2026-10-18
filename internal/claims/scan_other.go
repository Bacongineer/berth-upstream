//go:build !linux && !windows

package claims

import "errors"

// Scan has no process table on this platform yet; claims stay idle. The
// node still leases engines.
func Scan() ([]Proc, error) { return nil, errors.ErrUnsupported }
