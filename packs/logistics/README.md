# Logistics pack

Delivery performance against fleet capacity and dock congestion. It answers:
*which customers are behind a late shipment, and what would more vehicles or a
rebalanced yard do to the on-time rate?*

Gaps it is judged on: on-time rate, dock wait, vehicles available, peak load and
cost per shipment.

```bash
./bin/zyntra plan -f packs/logistics
./bin/zyntra scenario compare -f packs/logistics spot=add_spot_vehicles docks=rebalance_docks both=add_spot_vehicles+rebalance_docks
./bin/zyntra ontology impact -f packs/logistics Vehicle:tms:v-06
```

The edge weights are starting points, not measurements. After changes have run
with `ZYNTRA_EXECUTE=apply`, `zyntra calibrate -f packs/logistics` backtests them.
All three actions are advisory or write a sheet; none dispatches anything.
