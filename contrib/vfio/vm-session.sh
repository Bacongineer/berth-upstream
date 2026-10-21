#!/bin/sh
# Copyright 2026 Trevin Tindall (T-Crypt)
# SPDX-License-Identifier: Apache-2.0
#
# vm-session: give the governed GPU to a libvirt VM for one session, and give it
# back to your engines when the VM shuts down. An example to adapt; Berth does
# not install it. Guide: docs/vfio.md.
#
#   vm-session            start the VM session (drains the card first)
#   vm-session session    the part berth vfio run runs; do not call it yourself
#
# What happens:
#   1. berth vfio run drains your engines at their boundaries and holds the card.
#   2. --before stops the services that keep the card open without a lease
#      (a monitor running nvidia-smi in a loop, a desktop widget).
#   3. Berth checks that nothing holds the card any more, then runs `session`:
#      libvirt switches the card to vfio-pci (managed hostdevs, see
#      domain-hostdev.xml), the VM boots, and the session waits for it to shut
#      down. sudo asks for your password.
#   4. Berth releases the card only once it is back on its host driver, then
#      --after starts those services again.
set -eu

# Change these three for your machine.
DOMAIN=${VM_DOMAIN:-win11}
UNITS=${VM_STOP_UNITS:-"llama-swap.service"}   # user units to stop for the session
BERTH=${BERTH:-berth}                           # a full path when run from a desktop keybind

case ${1:-start} in
start)
  exec "$BERTH" vfio run --reason "$DOMAIN" \
    --before "systemctl --user stop $UNITS" \
    --after "systemctl --user start $UNITS" \
    -- "$0" session
  ;;
session)
  echo "devices for $DOMAIN: $BERTH_VFIO_DEVICES"
  sudo virsh start "$DOMAIN"
  while [ "$(sudo virsh domstate "$DOMAIN")" != "shut off" ]; do
    sleep 5
  done
  ;;
*)
  echo "usage: vm-session [start]" >&2
  exit 2
  ;;
esac
