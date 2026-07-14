# Cora Guanbai v0 Rule Review

Review date: 2026-07-13

## at_06 decision

Keep `at_06` as `attention`.

The six historical rows promoted from `ignore` were aggregated without exposing
message, exception, trace, or payload content. All six have unique row IDs and
the same stable shape:

| Historical label | Historical rule | Historical category | Source | Class | Method | Rows |
| --- | --- | --- | --- | --- | --- | ---: |
| ignore | none | 未匹配任何规则 | order.log | SettleClaimCasesServiceImpl | addClaimCases | 6 |

The historical label was therefore an unmatched default, not an explicit noise
rule. The event identifies a failed claim-case push in a core business chain.
Cora should keep it visible until real-service feedback demonstrates that it is
expected noise.

## Safety boundary

This review records only aggregate metadata. Raw production messages,
stacktraces, trace IDs, and payloads are not copied into Cora.
