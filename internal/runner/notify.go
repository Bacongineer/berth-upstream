// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// notifications keeps the service manager's sender in this wrapper while
// its engine runs in a separate scope. Only the child gets the private socket.
type notifications struct {
	env    []string
	failed <-chan error
	stop   func() error
}

func (n *notifications) close() error {
	if n == nil {
		return nil
	}
	return n.stop()
}

func (n *notifications) watch(done <-chan struct{}, fail func(error)) <-chan error {
	result := make(chan error, 1)
	if n == nil {
		result <- nil
		return result
	}
	go func() {
		select {
		case err := <-n.failed:
			select {
			case <-done:
			default:
				fail(err)
			}
			result <- err
		case <-done:
			select {
			case err := <-n.failed:
				result <- err
			default:
				result <- nil
			}
		}
	}()
	return result
}
