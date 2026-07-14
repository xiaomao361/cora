# Cora Shadow Evaluation

Source hash: `6f1de3879fe31bcebf534fa0960b2b6c1d4962c8c26a3005d11ebe648a6cc66d`
Model: `cora@0.1.0`
Experience: `cora-gbjk-v0.1.0`

## Dataset and grain

- Rows: 1404
- Unique Cora fingerprints: 20
- Duplicate fingerprint rows: 1384 (98.6%)
- Labels: attention=45, observe=0, ignore=1359
- Sources: 9
- Parsed full timestamps: 0/1404 (0.0%)
- Exception/stack populated: 1/1404 (0.1%)
- Time split available: false

## Row-level shadow results

| Metric | Value |
| --- | ---: |
| attention decisions | 31 |
| observe decisions | 559 |
| ignore decisions | 814 |
| decisive coverage | 60.2% |
| agreement among decisive rows | 99.3% |
| attention recall | 55.6% |
| old attention downgraded to ignore | 0 |
| old ignore promoted to attention | 6 |

## Signature and rule quality

- Signatures with conflicting historical labels: 1
- Signatures whose Cora decision changes across samples: 1
- Stable decisive signatures: 18
- Agreement on stable decisive signatures: 94.4%
- Rows matching more than one rule: 1
- Maximum rules matched by one row: 2

## Data-quality judgment

- **High:** Time-based validation is blocked: timestamps are incomplete or not full dates; row order is not a valid temporal substitute.
- **High:** Strict CSV parsing failed and compatibility parsing was required: parse error on line 377, column 145: bare " in non-quoted-field.
- **High:** Row-random model evaluation is leakage-prone because 98.6% of rows repeat an existing Cora fingerprint.
- **High:** Exception/stack data is missing on 99.9% of rows, so production fingerprint fidelity and exception-based rule coverage cannot be validated.
- **Medium:** 1 fingerprints change Cora decision across representative samples; latest-sample reevaluation can change queue state.

## Decision transitions by rule

| Transition | Rule | Rows |
| --- | --- | ---: |
| ignore->observe | at_04 | 534 |
| attention->observe | at_05 | 20 |
| ignore->attention | at_06 | 6 |
| ignore->observe | cora.default.unmatched | 5 |

## Redacted disagreement samples

No raw message, exception, trace ID, or payload is included.

| Fingerprint | Source | Line | Old label | Cora | Rule |
| --- | --- | ---: | --- | --- | --- |
| `20ba538d9c3be89264d1b8abb5f03605` | hut.log | 75757 | ignore | observe | cora.default.unmatched |
| `20ba538d9c3be89264d1b8abb5f03605` | hut.log | 76016 | ignore | observe | cora.default.unmatched |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 642 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 2190 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 8554 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 16051 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 16886 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 18056 | ignore | observe | cora.default.unmatched |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 24503 | attention | observe | at_05 |
| `24ea31c37a3471dbb04f26486b9bb3b3` | order.log | 32124 | ignore | attention | at_06 |
| `24ea31c37a3471dbb04f26486b9bb3b3` | order.log | 32315 | ignore | attention | at_06 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 32513 | attention | observe | at_05 |
| `24ea31c37a3471dbb04f26486b9bb3b3` | order.log | 36706 | ignore | attention | at_06 |
| `24ea31c37a3471dbb04f26486b9bb3b3` | order.log | 36897 | ignore | attention | at_06 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 37095 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 43784 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 52889 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 57937 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 63078 | attention | observe | at_05 |
| `20ba538d9c3be89264d1b8abb5f03605` | order.log | 64803 | attention | observe | at_05 |
