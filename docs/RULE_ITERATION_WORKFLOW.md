# Rule iteration workflow

`cora-iterate` is a read-only, offline review workflow. It freezes a stable Case
export, captures the selected business day's Problem snapshot, joins optional
code/release evidence, and writes immutable artifacts.

Required inputs are Server URL/token, product line, business date, timezone, and
an explicit private Pack manifest:

```sh
cora-iterate \
  -server-url https://cora.example.com \
  -auth-token-file /etc/cora/auth.token \
  -product-line payments \
  -business-date 2026-07-16 \
  -timezone UTC \
  -pack-manifest /private/cora/payments-model.json
```

The workflow may propose candidates from repeated, consistent, handled Cases.
It never modifies the running Server or activates a rule automatically. A
candidate requires business review, code/release evidence when relevant, a Pack
version change, deterministic tests, and shadow evaluation before deployment.

Generated artifacts and labeled datasets commonly contain private operational
facts. Keep them outside the public Cora repository.
