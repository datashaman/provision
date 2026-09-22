# Make component ownership explicit

Every stateful or supporting component must be declared as managed or external. Provision may create, update, and delete a managed component within documented limits, while an external component is only validated and bound; making ownership explicit prevents accidental mutation or deletion without forcing all teams into either fully managed or bring-your-own infrastructure.

