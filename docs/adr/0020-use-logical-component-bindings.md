# Use logical component bindings

Components refer to one another by logical identity through requires or uses relationships rather than embedding environment-specific addresses or credentials. Provision resolves those relationships into bindings for each environment and validates missing references and activation cycles. Endpoints remain attachments that expose HTTP or realtime components rather than becoming infrastructure-shaped components. This preserves portable application relationships while allowing implementations to supply materially different connection details.
