# Make blue-green fallback explicit

Each component declares a rollout mode of required, preferred, or replace. Required mode fails validation when blue-green behavior is unavailable; preferred mode may fall back to replacement only with explicit approval; replace mode permits an in-place update. Candidate activation is gated by destination verification and approval policy. This preserves the goal of blue-green behavior wherever possible without silently claiming guarantees an implementation cannot provide.
