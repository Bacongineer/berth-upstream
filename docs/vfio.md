# VFIO passthrough (experimental)

Linux only and experimental. It has passed its tests but has not yet run on a real passthrough, so the
commands and messages can still change.

Passing the governed card to a virtual machine through VFIO takes it away from the host for the whole
VM session. The NVIDIA driver lets go, `vfio-pci` takes the card, and nothing on the host may touch it
until the VM is done. Berth coordinates its side of that handover. It drains your engines, holds the
card, runs your own switch script, and keeps the next engine waiting until the card is back on its host
driver.

## What Berth does

- Drains every engine on the card at its boundary, as for any other lease.
- Holds the card for the whole session with a pinned lease at hold priority, so nothing else starts on
  it and nothing drains it.
- Stops berthd's own `nvidia-smi` probe for the whole hold, then checks which processes still hold
  `/dev/nvidia*` or the card's DRM nodes. Any holder stops the run, and Berth names it by PID and
  device node.
- Runs your script with the terminal attached, so its sudo prompt and the VM's console reach you, and
  with `BERTH_VFIO_DEVICES` set to the card's PCI functions.
- After the script exits, waits until every function of the card is off `vfio-pci` and the card
  answers `nvidia-smi`, and only then releases the hold.

## What Berth leaves to you

- The driver switch. Berth never binds or unbinds a driver and never writes to sysfs.
- Passwords. Your script calls sudo, and the prompt comes to your terminal.
- The VM, libvirt, QEMU and every other device you pass through.
- Processes that hold the card. Berth names them and refuses; it never stops them.

## Requirements

- An IOMMU. Turn on VT-d (Intel) or AMD-Vi in the firmware, and add `intel_iommu=on iommu=pt` (or
  `amd_iommu=on iommu=pt`) to the kernel command line.
- A second GPU or an iGPU for the host's display, because the passed-through card leaves the host.
- A clean IOMMU group. The card's group may hold only the card's own functions, such as its audio
  function. `berth vfio status` lists the group's members and their drivers, and `berth vfio run`
  refuses while anything else is in the group.
- An NVIDIA card. AMD cards are not covered yet.

## The commands

`berth vfio status` reads the card's state and changes nothing. It shows whether IOMMU is on, the IOMMU
group and the driver bound to each function, the processes holding the card's device nodes, and what
berthd reports. While the card is bound to `vfio-pci`, `nvidia-smi` cannot see it, so pass the card's
address yourself (`berth vfio status 0000:01:00.0`).

`berth vfio run [--reason NAME] [--before CMD] [--after CMD] -- CMD` holds the card and runs your
script:

```sh
berth vfio run --reason galaxy \
  --before "systemctl --user stop llama-swap.service" \
  --after "systemctl --user start llama-swap.service" \
  -- ~/bin/gpu-to-galaxy
```

Engines drain first. Once the card is granted, Berth runs `--before`, if you gave one. Use it to stop
your own services that keep the card open without a lease, such as a monitor that runs `nvidia-smi` in
a loop. berthd stops its own probe for the hold. After a few seconds Berth reads the device holders. If
any process still holds the card, Berth names it, releases the hold and exits 1. A process whose open files Berth cannot read (another user's, usually
root's) gets a warning instead, because Berth cannot prove it clear. Such a process can block the unbind.

When your script exits, Berth releases the hold only if the card is back. Otherwise it keeps the hold,
names the function still on `vfio-pci`, and waits. Each Ctrl-C checks again and releases only once the
card is back. `berth vfio run` exits with your script's exit code.

`--after` runs once the hold has ended, whenever `--before` ran. That covers the card coming back, a
refusal and a failed `--before`. What `--before` stopped always comes back, and never while the card
is still in the VM. Both run with `sh -c`, the terminal attached and `BERTH_VFIO_DEVICES` set.

While the hold is granted, berthd stops opening the card and `berth status` shows it as passed through
to a VM. Waiting requests are told why.

## Examples to adapt

[contrib/vfio](https://github.com/T-Crypt/berth/tree/main/contrib/vfio) has a complete launcher for a
libvirt VM (`vm-session.sh`) and the GPU part of the domain (`domain-hostdev.xml`). With managed host
devices, libvirt switches the drivers, and the launcher only starts the VM and waits for it.

Without libvirt's managed devices, your script switches the drivers itself, as below.

This is an example, not something Berth installs. It moves every function the generic way
(`driver_override`), so it works for the card's audio function too, and it reads the addresses from
`BERTH_VFIO_DEVICES`. If your libvirt domain marks the card's `hostdev` as `managed='yes'`, libvirt
switches the drivers itself, and the script only starts the VM and waits.

```sh
#!/bin/sh
# gpu-to-galaxy: give the card to the VM, run it, take the card back.
# Run it through: berth vfio run --reason galaxy -- ~/bin/gpu-to-galaxy
set -eu
devs="${BERTH_VFIO_DEVICES:?run this through berth vfio run}"

to() { # to DRIVER: move every function of the card to DRIVER ("" = its own driver)
  for d in $devs; do
    echo "$1" | sudo tee "/sys/bus/pci/devices/$d/driver_override" >/dev/null
    if [ -e "/sys/bus/pci/devices/$d/driver" ]; then
      echo "$d" | sudo tee "/sys/bus/pci/devices/$d/driver/unbind" >/dev/null
    fi
    echo "$d" | sudo tee /sys/bus/pci/drivers_probe >/dev/null
  done
}

sudo modprobe vfio-pci
to vfio-pci
sudo virsh start galaxy
while [ "$(sudo virsh domstate galaxy)" != "shut off" ]; do sleep 5; done
to ""
```

If the script dies with the card still on `vfio-pci`, the hold stays, and the next engine waits until
the card is back.
