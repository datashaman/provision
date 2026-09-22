# Make component ownership explicit

Every stateful or supporting component must be declared as managed or external. Provision may create, update, and delete a managed component within documented limits, while an external component is only validated and bound. Managed stateful components default to retention when their environment is destroyed, and changing an external component to managed requires an explicit adoption operation that verifies identity and shows the newly assumed lifecycle consequences. These rules prevent accidental mutation or deletion without forcing all teams into either fully managed or bring-your-own infrastructure.

