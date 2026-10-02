# Shop pack: counter and stock

One shop, fed from CSV exports. No Kubernetes, no SaaS: point the file sources at your POS, stock, settlement and supplier exports and Zyntra ranks what to do next.

## The three gaps this pack is judged on

| Gap | Owner | KPI | Target |
| --- | --- | --- | --- |
| Shelves are empty | buyer | `stockout_rate` (share of SKUs with zero stock) | at most 2% |
| The counter queue is long | floor | `queue_wait` (average minutes) | at most 4 |
| Slow movers lock up cash | buyer | `dead_stock_days` (days since a slow mover last sold) | at most 45 |

Margin, sales, settled cash and supplier lead time are tracked too. Margin is protected by an invariant: an action whose simulation cuts gross margin by more than 3% is dropped from the plan.

## Try it on the fixture

```sh
zyntra pack validate packs/shop
zyntra gaps -f packs/shop
zyntra plan -f packs/shop
zyntra plan -f packs/shop -owner floor
zyntra simulate -f packs/shop -action reorder_fast_movers
```

On the sample day the plan closes the stockout gap with `reorder_fast_movers` (a purchase order file) and refuses `markdown_dead_stock` because a 30% markdown would cut margin by 6%. The capped 10% markdown stays in the plan.

One simulate trace:

```text
$ zyntra simulate -f packs/shop -action open_second_counter

KPI            BEFORE  AFTER   RANGE               CHANGE   TARGET
queue_wait     6.5     2.6     1.82..3.38          -60.0%   met (closed)
daily_sales    104000  107120  105248..109616      +3.0%    missed
cash_on_hand   48000   48432   48120.96..49010.88  +0.90%   missed
cashiers_open  1       2       -                   +100.0%  -

Why:
  open_second_counter changes cashiers_open by +1 (direct effect, absolute)
  cashiers_open +100.0% -> queue_wait -60.0% (weight -0.6 ±20%): a second counter roughly halves the wait; it cannot go below zero
  queue_wait -60.0% -> daily_sales +3.0% (weight -0.05 ±50%): long queues cause walk-outs
  daily_sales +3.0% -> cash_on_hand +0.90% (weight +0.3 ±30%): sales settle into cash

Closes: queue_wait
```

## Actions

| Action | Runs as | Guard |
| --- | --- | --- |
| `reorder_fast_movers` | file: a purchase order under `po/` | precondition: stockout worse than 2%; only in `buy-hours`; compensate: `cancel_open_po` |
| `markdown_dead_stock` | webhook to `${ZYNTRA_POS_URL}/markdowns` | margin invariant (blocked on the fixture) |
| `markdown_capped` | webhook, 10% markdown | margin invariant; compensate: `reverse_markdown` |
| `open_second_counter` | noop: people do it, the approval is recorded | only in the `evening` window |
| `drop_slow_supplier` | file: next buy list | none |

Compensating actions are linked on the proposal and never run on their own.

## Rules

`zyntra serve` checks these lines against the live plan (Model page → Pack rules check, or `/api/v1/ai/contradictions`). Keep them true or change the YAML.

- `reorder_fast_movers` runs only in `buy-hours`, precondition: `stockout_rate`, compensate: `cancel_open_po`.
- `open_second_counter` runs only in `evening`.
- `markdown_capped` must be undone by compensate: `reverse_markdown`.
- Never approve `markdown_dead_stock` while it breaks the margin invariant.

## Running a pilot

1. Replace the files in `fixture/` with your exports (same columns, see `sources.example.yaml`), or change the `file:` paths.
2. Run `zyntra serve -f packs/shop` for five business days. Gaps and the plan are the demo; leave execution on dry-run.
3. Enter the number of counters open in the console (Signals → Manual values); it is a manual KPI and every entry is audited.
4. Approve one low-risk action with `ZYNTRA_EXECUTE=apply` and `ZYNTRA_OUTPUT_DIR` pointing at a test folder. Compare predicted and actual the next morning in the decision record.
5. Only then set `ZYNTRA_POS_URL` and point the webhooks at the real POS, still inside a window and with a named approver.

Samples outside shop hours (09:00 to 21:30, Asia/Kolkata) do not count: a shop that misses target at 2am is not a gap. Weights are starting points; edit them as you learn.
