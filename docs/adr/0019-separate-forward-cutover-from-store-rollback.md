# Separate forward cutover from store rollback

Required store blue-green guarantees that no acknowledged write is lost during forward cutover, using continuous synchronization or a declared bounded write pause. Rollback after new writes reach the candidate is classified separately as zero-loss, bounded-loss, or forward-only; retaining an old generation does not by itself make rollback safe. This distinction prevents a nominal rollback window from hiding stale data while allowing environments to choose the recovery guarantee their implementation and risk profile can support.
