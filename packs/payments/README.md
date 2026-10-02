# Payments pack

Approval rate, authorisation latency, settlement backlog and fraud-review load
for a payments processor. It answers: *which merchants sit behind a slow
acquirer or a deep settlement queue, and what would a failover or more workers
do to approvals?*

```bash
./bin/zyntra plan -f packs/payments
./bin/zyntra scenario compare -f packs/payments failover=failover_to_backup_acquirer workers=scale_settlement_workers
./bin/zyntra ontology impact -f packs/payments Acquirer:gw:acq-a
```

Only ids, statuses and timings are read; keep card numbers and other
cardholder data out of the exports. The failover action carries a hard
invariant: it is blocked if the simulation says approvals would fall by more
than two points. Every action here is advisory (noop): approving records the
decision and a person makes the change.
