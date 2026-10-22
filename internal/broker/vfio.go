// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// The card's VM passthrough state. While the governed card is in a VM (or
// on its way to or from one) the probe cannot answer, so the driver bound
// to the card's PCI function says where the card is. Everything here is
// read-only against the machine: the switch itself is the operator's,
// guarded by a VFIO hold, a pinned acquire in Group "vfio".

import (
	"fmt"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/proto"
)

// vfioHold is the granted lease in Group "vfio" that guards a switch, or
// nil. A releasing or stuck hold has ended; the probe reads the card again.
// Called with b.mu held.
func (b *Broker) vfioHold() *lease {
	for _, l := range b.leases {
		if l.group == proto.GroupVFIO && l.state != releasing && l.state != stuck {
			return l
		}
	}
	return nil
}

// holdTick is the tick while a VFIO hold is granted: the probe is never
// called, the card state comes from the driver bound to the function, and
// schedule grants nothing new. The hold itself stays HOT. Called with b.mu
// held.
func (b *Broker) holdTick() {
	if b.cardAddr == "" {
		// berthd never saw the card: it can still find the one function
		// the VM's passthrough bound, the same scan a probe failure runs.
		b.cardAddr = gpu.FindVFIOCard()
	}
	if d := gpu.DriverFor(b.cardAddr); d == gpu.VFIODriver {
		b.setCard(proto.CardPassthrough, d)
	} else {
		b.setCard(proto.CardDetaching, d)
	}
	b.schedule(b.now())
}

// vfioBound reads the governed card's driver after a probe failure:
// vfio-pci when the card is in a VM (switched by hand), "" otherwise. When
// no reading ever named the card's address, it is found by scan and
// remembered. Called with b.mu held.
func (b *Broker) vfioBound() string {
	if b.cardAddr == "" {
		b.cardAddr = gpu.FindVFIOCard()
	}
	if b.cardAddr == "" || gpu.DriverFor(b.cardAddr) != gpu.VFIODriver {
		return ""
	}
	return gpu.VFIODriver
}

// setCard records the card state and emits one event per transition into
// or out of passthrough. Called with b.mu held.
func (b *Broker) setCard(state, driver string) {
	if b.cardState == state && b.cardDriver == driver {
		return
	}
	switch {
	case state == proto.CardPassthrough && b.cardState != proto.CardPassthrough:
		b.log.Info("card passed through to a VM", "driver", driver, "addr", b.cardAddr)
		b.emit(proto.Event{Kind: proto.EvCardPassthrough, Detail: driver})
	case b.cardState == proto.CardPassthrough && state != proto.CardPassthrough:
		b.log.Info("card returned from the VM", "driver", driver)
		b.emit(proto.Event{Kind: proto.EvCardReturned, Detail: driver})
	}
	b.cardState, b.cardDriver = state, driver
}

// cardWhy is the reason every waiter reads while the card does not serve
// the node: where the card is and what brings it back. Called with b.mu
// held.
func (b *Broker) cardWhy() string {
	if b.cardState == proto.CardDetaching {
		return fmt.Sprintf("%s is being passed through to a VM; it comes back when the VM's hold ends", b.cardName())
	}
	return fmt.Sprintf("%s is passed through to a VM (%s); it comes back when the VM's hold ends", b.cardName(), b.cardDriver)
}

// cardName is how messages name the governed card: gpuN once a reading
// placed it, its PCI address while berthd has never read it (it started
// with the card in a VM, and gpu0 can be another card). Called with b.mu
// held.
func (b *Broker) cardName() string {
	if b.snap.Name == "" && b.cardAddr != "" {
		return "the card at " + b.cardAddr
	}
	return fmt.Sprintf("gpu%d", b.snap.Index)
}
