// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package units carries the systemd unit files that sit beside it in
// contrib/ to the Go code that renders them, so these files stay the one
// source of the units' text: the README's copy steps and `berth service
// install` read the same words.
package units

import (
	_ "embed"
)

//go:embed berthd.service
var berthd string

//go:embed berth-gateway.service
var berthGateway string

//go:embed berth-harbor.service
var berthHarbor string

var texts = map[string]string{
	"berthd":        berthd,
	"berth-gateway": berthGateway,
	"berth-harbor":  berthHarbor,
}

// Text returns the unit file's text for one of the three unit names.
func Text(name string) (string, bool) {
	t, ok := texts[name]
	return t, ok
}
