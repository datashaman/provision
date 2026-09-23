# Handoff queue generations with producer fencing

Changing workers leaves their logical Queue and physical generation in place. Replacing the Queue temporarily creates a candidate generation and requires a producer admission fence, in-flight drain or release, destination-confirmed transfer of remaining messages, verification, and consumer and producer rebinding. This makes at-least-once preservation explicit while rejecting required blue-green when an implementation cannot control producers or account for accepted messages.
