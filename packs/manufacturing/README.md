# Manufacturing pack

Inspection capacity against order deadlines. It answers: *which orders are at
risk because inspection is backed up, and what would each fix do?*

Gaps this pack is judged on: inspection backlog, inspection turnaround,
orders at risk, GPU capacity and inspection cost.

Try it on the fixtures:

```bash
./bin/zyntra ontology validate -f packs/manufacturing
./bin/zyntra ontology impact -f packs/manufacturing Cluster:infra:gpu-a
./bin/zyntra scenario compare -f packs/manufacturing \
  gpus=add_gpu_capacity site=alternate_inspection_site both=add_gpu_capacity+alternate_inspection_site
ZYNTRA_API_KEY=dev ./bin/zyntra serve -f packs/manufacturing
```

The workflow: orders (ERP), production status (MES) and compute (infra) are
linked in `ontology.yaml`. When the inspection backlog fails its target, the
console shows the orders that depend on that service. Typed actions
(`add_gpu_capacity`, `alternate_inspection_site`, `reschedule_training`) take
the affected service or cluster as an input, need an approver, and are checked
afterwards: the service must be back within target.

The business links explain *what is connected*. Predictions come only from the
KPI edges in `kpis.yaml`, which are declared, not learned.
