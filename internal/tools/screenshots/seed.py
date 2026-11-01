"""Writes the demo data for the screenshots (emulator YAML) to stdout.

Fictional: an online shop "acme" with a production and a staging project.
"""
import random
from datetime import date, datetime, timedelta

random.seed(7)
out = []
w = out.append

COUNTRIES = [("CZ", ["Prague", "Brno", "Ostrava"]), ("DE", ["Berlin", "Munich", "Hamburg"]),
             ("PL", ["Warsaw", "Krakow"]), ("AT", ["Vienna", "Graz"]), ("SK", ["Bratislava"]),
             ("NL", ["Amsterdam", "Utrecht"])]
FIRST = ["Anna", "Jakub", "Eva", "Lukas", "Marie", "Tomas", "Lena", "Jonas", "Petra", "Filip",
         "Sofia", "Martin", "Klara", "David", "Nina", "Adam", "Ema", "Ondrej", "Julia", "Matej"]
LAST = ["Novak", "Schmidt", "Kowalski", "Huber", "Horvath", "de Vries", "Svoboda", "Weber",
        "Nowak", "Bauer", "Kral", "Jansen", "Dvorak", "Fischer", "Wojcik"]
STATUS = ["delivered"] * 7 + ["shipped"] * 2 + ["cancelled", "refunded"]
SKUS = ["MUG-01", "TEE-BLK-M", "TEE-WHT-L", "CAP-02", "BAG-TOTE", "SOCK-3P", "HOOD-GRY-M", "BOTTLE-05"]


def table(name, spec, rows):
    w(f"    - id: {name}")
    w("      columns:")
    for col, typ, mode in spec:
        w(f"      - name: {col}")
        w(f"        type: {typ}")
        if mode:
            w(f"        mode: {mode}")
    w("      data:" if rows else "      data: []")
    for r in rows:
        first = True
        for (col, _, _), v in zip(spec, r):
            if v is None:
                continue
            if isinstance(v, str):
                v = "'" + v.replace("'", "''") + "'"
            w(f"      {'- ' if first else '  '}{col}: {v}")
            first = False


def customers(n):
    rows = []
    for i in range(n):
        c, cities = random.choice(COUNTRIES)
        f, l = random.choice(FIRST), random.choice(LAST)
        email = f"{f}.{l}".lower().replace(" ", "") + f"{i}@example.com"
        rows.append([f"C{1000 + i}", f"{f} {l}", email, c, random.choice(cities),
                     (date(2023, 1, 1) + timedelta(days=random.randint(0, 600))).isoformat()])
    return rows


def orders(n, cust):
    rows = []
    t0 = datetime(2026, 10, 1, 8, 0)
    for i in range(n):
        c = random.choice(cust)
        ts = t0 + timedelta(minutes=random.randint(0, 31 * 24 * 60))
        rows.append([50000 + i, c[0], c[3], random.choice(STATUS),
                     f"{random.randint(900, 25000) / 100:.2f}", "EUR", ts.strftime("%Y-%m-%dT%H:%M:%SZ")])
    rows.sort(key=lambda r: r[6])
    return rows


CUST = [("customer_id", "STRING", "REQUIRED"), ("name", "STRING", None), ("email", "STRING", None),
        ("country", "STRING", None), ("city", "STRING", None), ("signup_date", "DATE", None)]
ORD = [("order_id", "INT64", "REQUIRED"), ("customer_id", "STRING", None), ("country", "STRING", None),
       ("status", "STRING", None), ("amount", "NUMERIC", None), ("currency", "STRING", None),
       ("created_at", "TIMESTAMP", None)]
ITEMS = [("order_id", "INT64", "REQUIRED"), ("sku", "STRING", None), ("quantity", "INT64", None),
         ("unit_price", "NUMERIC", None)]

w("projects:")
for proj, n_c, n_o in [("acme-prod", 240, 1800), ("acme-staging", 30, 120)]:
    cust = customers(n_c)
    ords = orders(n_o, cust)
    w(f"- id: {proj}")
    w("  datasets:")
    w("  - id: sales")
    w("    tables:")
    table("customers", CUST, cust)
    table("orders", ORD, ords)
    table("order_items", ITEMS, [[o[0], random.choice(SKUS), random.randint(1, 3),
                                  f"{random.randint(500, 6000) / 100:.2f}"] for o in ords[:400]])
    if proj == "acme-staging":
        w("  - id: tmp_backfill")
        w("    tables:")
        table("orders_copy", ORD, ords[:20])
        continue
    w("  - id: marketing")
    w("    tables:")
    table("campaigns", [("campaign_id", "STRING", "REQUIRED"), ("name", "STRING", None), ("channel", "STRING", None),
                        ("start_date", "DATE", None), ("budget", "NUMERIC", None)],
          [[f"CMP-{i:03d}", n, ch, f"2026-{m:02d}-01", f"{b}.00"] for i, (n, ch, m, b) in enumerate([
              ("Autumn sale", "email", 10, 12000), ("Black Friday", "search", 11, 40000),
              ("New hoodies", "social", 9, 8000), ("Loyalty push", "email", 8, 5000)])])
    table("ad_spend", [("day", "DATE", None), ("campaign_id", "STRING", None), ("clicks", "INT64", None),
                       ("cost", "NUMERIC", None)],
          [[(date(2026, 10, 1) + timedelta(days=d)).isoformat(), f"CMP-{c:03d}", random.randint(200, 4000),
            f"{random.randint(5000, 90000) / 100:.2f}"] for d in range(31) for c in range(3)])
    w("  - id: web_events")
    w("    tables:")
    for d in range(28, 32):
        table(f"events_202610{d}", [("user_id", "STRING", None), ("event_name", "STRING", None),
                                    ("page", "STRING", None), ("event_ts", "TIMESTAMP", None)],
              [[f"u{random.randint(1, 900)}", random.choice(["page_view", "add_to_cart", "purchase", "search"]),
                random.choice(["/", "/shop", "/shop/hoodies", "/cart", "/checkout"]),
                f"2026-10-{d}T{random.randint(0, 23):02d}:{random.randint(0, 59):02d}:00Z"] for _ in range(40)])
    w("  - id: finance")
    w("    tables:")
    table("invoices", [("invoice_id", "STRING", "REQUIRED"), ("order_id", "INT64", None), ("issued_on", "DATE", None),
                       ("total", "NUMERIC", None), ("vat_rate", "NUMERIC", None)],
          [[f"INV-{o[0]}", o[0], o[6][:10], o[4], "0.21"] for o in ords[:300]])
    table("payments", [("payment_id", "STRING", "REQUIRED"), ("invoice_id", "STRING", None), ("method", "STRING", None),
                       ("paid_at", "TIMESTAMP", None), ("amount", "NUMERIC", None)],
          [[f"PAY-{o[0]}", f"INV-{o[0]}", random.choice(["card", "card", "paypal", "bank_transfer"]), o[6], o[4]]
           for o in ords[:280]])
print("\n".join(out))
