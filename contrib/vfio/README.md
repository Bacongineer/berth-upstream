# VFIO examples

Examples to adapt for passing the GPU Berth governs to a libvirt VM. Berth installs none of them.
[docs/vfio.md](../../docs/vfio.md) explains what Berth does during a passthrough and what it leaves to you.

| File | What it is |
|---|---|
| [vm-session.sh](vm-session.sh) | A launcher. `berth vfio run` drains the card, `--before` stops your own services that hold it, libvirt boots the VM, and Berth gives the card back to your engines once the VM is off |
| [domain-hostdev.xml](domain-hostdev.xml) | The GPU part of a libvirt domain, with managed host devices so libvirt switches the drivers |

To try it:

1. Check the card can pass through: `berth vfio status` should show IOMMU on and an IOMMU group that
   holds only the card's own functions.
2. Add the two `<hostdev>` entries to your domain with `virsh edit`, using the addresses
   `berth vfio status` printed.
3. Copy `vm-session.sh`, set `DOMAIN` and `UNITS` at the top, and run it from a terminal first, so you
   see sudo's prompt and Berth's messages.

Run `berth vfio status` again while the VM is up: any process it lists as holding the card belongs in
`UNITS`.
