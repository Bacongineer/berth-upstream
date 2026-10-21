# Benchmarks and tested hardware

## Tested hardware

The scenarios in [contrib/chaos](https://github.com/T-Crypt/berth/blob/main/contrib/chaos/README.md) and the numbers below are how
Berth's failure handling gets checked outside this tree. Run them on your card and add a row
with the result, or report what happened with the
[drain report issue form](https://github.com/T-Crypt/berth/issues/new?template=drain-report.yml)
and the table credits you here.

| GPU | VRAM | Driver | OS | Engines | Result | Tester | Date |
|---|---|---|---|---|---|---|---|
| RTX 4090 | 24 GB | 615.78.08 | Arch Linux | llama-swap + NInfer + Strata + ComfyUI | daily use | T-Crypt | 2026-10-20 |
| RTX 4090 | 24 GB | 615.78.08 | Arch Linux | contrib/chaos scenarios 1, 2, 4 | pass | T-Crypt | 2026-10-20 |
| RTX 4090 | 24 GB | 615.78.08 | Arch Linux | contrib/chaos scenario 3 (parent exits at once) | reported as a stowaway, never signalled: the known window | T-Crypt | 2026-10-20 |
| RTX 5070 Ti + RTX 3060 | 16 GB + 12 GB | - | Windows 11 | the node, claims and the harbor, no engines | found [#81](https://github.com/T-Crypt/berth/issues/81) and [#82](https://github.com/T-Crypt/berth/issues/82) | mesquitafmr | 2026-10-21 |

## Swap numbers

`contrib/bench/swap` through the gateway on the RTX 4090 workstation (driver 615.78.08, PCIe 4.0
x16, 62.5 GiB RAM, models on a PCIe NVMe SSD), 2026-10-20: an opening request to A, then three
cycles of B and A, a fixed prompt, greedy.

| Engine | Cold first token | Warm first token (median) | Engine's own load |
|---|---|---|---|
| NInfer-Qwen3.8-27B (llama-swap) | 12.2 s | 16.6 s | part of the first token |
| Strata, Qwen3.8-Flash-Next UD-IQ4_XS | 387.5 s | 379.2 s | 85 s cold, 71 to 75 s after |

Switching on every request is the ping-pong Berth's juggle guard exists to hold back: each switch
to Strata waited about 5 minutes behind the guard until llama-swap unloaded NInfer, and the
engine's own load (from the lease grant to its ready line) is the last column. The bench's
"output identical" check is left out: both models think before they answer, the 32 tokens it asks
for end inside the thinking, and it compared empty answers.

**Where Strata's load goes.** The model file is 87.2 GiB and Strata page-locks 40 GiB of RAM
while it runs, so on this 62.5 GiB machine its file never stays in the page cache (7% cached after
the run) and every start reads it from disk. Moving the file from a SATA SSD to the NVMe SSD cut
the load from 142 to 160 s to 54 to 85 s. Of today's 71 to 85 s, reading the weights and filling
the GPU's expert cache took about 18 s; the rest is Strata's own start-up after it reports
"almost ready", which no disk or cache speeds up.

**Machines with RAM to spare.** Neither engine here can park, so these are cold reloads, not the
warm tier. On a machine with enough RAM to keep a model's file in the page cache (`berth models`
shows it as COOL, `berth prefetch` warms it), the disk part of a reload comes from RAM instead;
an engine that parks ([park in RAM](gateway.md#park-in-ram-warm-switch)) skips the reload. Neither number
is measured yet: the reference machine has no RAM to spare beside Strata. If you run 128 GB or
more, run the bench with park on and add a row.
