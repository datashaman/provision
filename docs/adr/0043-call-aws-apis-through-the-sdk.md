# Call AWS APIs through the SDK

AWS implementation adapters call provider APIs through the AWS SDK for Go while Provision owns the canonical Plan, Recorded State, identities, and recovery journal. CloudFormation may be an internal mechanism for an adapter when a stack provides leverage, but it does not become the universal planning or state engine; Terraform and Pulumi are not embedded initially. This avoids competing state and planning models while retaining provider-native control for blue-green and recovery workflows.
