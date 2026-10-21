// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package vfio

import "errors"

// ErrLinuxOnly is what the reader reports off Linux: the sysfs and /proc
// shapes it reads belong to Linux. The vfio commands refuse with it and
// change nothing.
var ErrLinuxOnly = errors.New("the VFIO state is readable on Linux only in this release")

// Supported is nil on Linux, ErrLinuxOnly elsewhere.
func (r Reader) Supported() error { return ErrLinuxOnly }

func (r Reader) IOMMUOn() (bool, error)                { return false, ErrLinuxOnly }
func (r Reader) Group(addr string) ([]Function, error) { return nil, ErrLinuxOnly }
func (r Reader) Driver(addr string) (string, error)    { return "", ErrLinuxOnly }
func (r Reader) Openers(addr string) ([]Opener, error) { return nil, ErrLinuxOnly }
