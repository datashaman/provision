# Use a pinned schedule applet

A stable systemd timer invokes a pinned, nonresident Provision schedule applet that records occurrences in a local SQLite ledger and starts generation-specific task units. The applet implements timezone, daylight-saving, overlap, retry, and missed-run policy beyond the timer's basic triggering. It remains available without the management CLI process and changes only through an auditable Plan, avoiding a resident control agent while preserving the Schedule contract.
