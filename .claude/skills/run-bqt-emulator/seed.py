"""Writes the emulator's seed data (YAML) to stdout.

sales: orders (700 rows, enough to page past 500), customers (37 rows);
events: a shard family events_20240101..03; empty_ds: no tables.
setup.sh adds a view and a partitioned table through the REST API, since the
YAML format cannot express them.
"""
import random

random.seed(1)
out = []
w = out.append


def cols(spec):
    w("      columns:")
    for name, typ, mode in spec:
        w(f"      - name: {name}")
        w(f"        type: {typ}")
        if mode:
            w(f"        mode: {mode}")


w("projects:")
w("- id: test-proj")
w("  datasets:")
w("  - id: sales")
w("    tables:")
w("    - id: orders")
cols([("id", "INT64", "REQUIRED"), ("customer_id", "STRING", None), ("amount", "NUMERIC", None),
      ("note", "STRING", None), ("created_at", "TIMESTAMP", None)])
w("      data:")
for i in range(700):
    w(f"      - id: {i + 1}")
    w(f"        customer_id: cust-{i % 37}")
    w(f"        amount: {random.randint(100, 99999) / 100}")
    w(f"        note: order number {i + 1} with a somewhat longer note to make the column wide")
    w(f"        created_at: '2024-01-{(i % 28) + 1:02d}T10:00:00Z'")
w("    - id: customers")
cols([("id", "STRING", None), ("name", "STRING", None), ("city", "STRING", None)])
w("      data:")
for i in range(37):
    w(f"      - id: cust-{i}")
    w(f"        name: Customer {i}")
    w(f"        city: City {i % 5}")
w("  - id: events")
w("    tables:")
for d in ["20240101", "20240102", "20240103"]:
    w(f"    - id: events_{d}")
    cols([("user_id", "STRING", None), ("event_name", "STRING", None)])
    w("      data:")
    for i in range(5):
        w(f"      - user_id: u{i}")
        w(f"        event_name: ev-{d}-{i}")
w("  - id: empty_ds")
w("    tables: []")
print("\n".join(out))
