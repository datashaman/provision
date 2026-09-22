# Anonymize production data before boundary crossing

A production-derived data refresh is coordinated and recorded by Provision, while application-defined actions perform extraction, anonymization, transformation, and loading. Raw production data must be anonymized within the production security boundary before it enters a non-production environment; when that cannot be demonstrated, the refresh fails closed. This sacrifices some convenience to prevent a temporary copy or failed transformation from exposing production data outside its permitted boundary.
