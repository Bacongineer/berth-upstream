// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package runner

func startNotifications() (*notifications, error) { return nil, nil }
