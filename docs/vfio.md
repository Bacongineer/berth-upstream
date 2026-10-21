# VFIO passthrough (experimental)

Linux only, and experimental: it has run on one machine (the reference RTX 4090
box) and nowhere else. The API and the messages can still change.

When you pass the governed card to a virtual machine through VFIO, the card
leaves the host for the whole VM session: the NVIDIA driver lets go, `vfio-pci`
takes the function, and nothing on the host may touch it until the VM is done.
Berth coordinates its side of that handover. It drains the engines first, holds
the card for you, wraps your own switch script, and does not let the next
engine load until the card is back on its host driver.

## What Berth does, and what it does not

Berth does:

- Drain every engine on the card at its own boundary, the way any lease does.
- Hold the card for the whole passthrough: a pinned lease at hold priority
  (group `vfio`), so nothing else starts on it and nothing drains it.
- Check before the switch that nothing but the driver holds the card: the
  processes with `/dev/nvidia*` or the card's DRM nodes open. Anyone but
  berthd's own probe refuses the run, by PID and device node.
- Run your script with the terminal attached, so its sudo prompt and the VM's
  console reach you, with `BERTH_VFIO_DEVICES` set to the card's PCI functions.
- Wait after the script exits until every function of the card is off
  `vfio-pci` and the card answers `nvidia-smi` again, and only then release.
- Refuse to release a card that is not back. Until the card is back, the hold
  stays; Ctrl-C rechecks and releases only when it is.

Berth does not:

- Bind or unbind a driver, or write to sysfs. Your script owns the switch.
- Ask for a password or keep one. Your script calls sudo itself, and the
  prompt reaches your terminal.
- Manage the VM, libvirt, QEMU or any other device in the machine. Your
  script and libvirt own those.
- Stop a process that holds the card. It names the holders and refuses; you
  decide what to do with them.

## Requirements

- A machine with an IOMMU: VT-d or AMD-Vi on in the firmware, and
  `intel_iommu=on iommu=pt` (or the AMD form) on the kernel command line.
  `berth vfio status` says whether IOMMU is on.
- A second GPU or an iGPU for the host display, since the passed-through card
  is out of the host's hands.
- The card's IOMMU group holding only the card's own functions (for a GPU,
  its audio function is fine). A group with anything else in it blocks the
  passthrough; `berth vfio status` names the members and their drivers, and
  `berth vfio run` refuses until the group is clean.
- An NVIDIA card. AMD cards are out of scope in this release.

## The commands

`berth vfio status` reads the card's state and changes nothing: IOMMU on or
off, the IOMMU group and the driver each function is bound to, the processes
holding the card's device nodes, and what berthd reports about the card. It
takes the card's PCI address when you have it (`berth vfio status
0000:01:00.0`), which is the only way to read the state while the card is
bound to `vfio-pci`, because `nvidia-smi` cannot see the card then.

`berth vfio run [--reason NAME] -- CMD` holds the card and runs your script:

    berth vfio run --reason galaxy -- ~/bin/gpu-to-galaxy

Engines drain first. When the card is granted, berthd's probe is given a few
seconds to go quiet, then the openers are read again: anyone else holding a
device node refuses the run, names itself, and releases the hold. Berth never
stops it. A process whose fd directory cannot be read is named as unreadable;
it is a warning, not a refusal, and the unbind fails where it holds the card.
When the script exits, the hold ends only when the card is back; otherwise the
hold stays and the message names the function that is still on `vfio-pci`, and
Ctrl-C rechecks and releases only once the card is back.

The exit code is your script's exit code.

## An example script to adapt

This is an example to adapt to your own machine, not something Berth installs.
It uses `BERTH_VFIO_DEVICES`, so no PCI address is hardcoded:

```sh
#!/bin/sh
# gpu-to-galaxy: pass the card to the VM, run it, bring the card back.
# Berth holds the card around this whole script; run it through
#   berth vfio run --reason galaxy -- ~/bin/gpu-to-galaxy
set -eu
DEVS="${BERTH_VFIO_DEVICES:?run through: berth vfio run -- ...}"

# The switch asks for your password once, through sudo.
for d in $DEVS; do
  echo "$d" | sudo tee /sys/bus/pci/drivers/nvidia/unbind > /dev/null
  echo "$d" | sudo tee /sys/bus/pci/drivers/vfio-pci/bind > /dev/null
done

# The VM holds the card until it is shut down.
virsh start galaxy
while [ "$(virsh domstate galaxy 2>/dev/null || echo gone)" != "shut off" ]; do
  sleep 5
done

for d in $DEVS; do
  echo "$d" | sudo tee /sys/bus/pci/drivers/vfio-pci/unbind > /dev/null
  echo "$d" | sudo tee /sys/bus/pci/drivers/nvidia/bind > /dev/null
done
```

A real script keeps the VM lifecycle its own way (a managed `virt-manager`
session, a `virsh shutdown` wait, hooks inside the libvirt domain); the shape
Berth needs is only this: switch, run the VM, switch back, and exit. If the
script dies with the card still on `vfio-pci`, the hold stays until the card
is back, and the next engine waits.

While a VFIO hold is granted, berthd pauses its own GPU probe (so its
nvidia-smi child never blocks the unbind) and reports the card as passed
through to a VM; waiters are told so instead of seeing a probe failure.
