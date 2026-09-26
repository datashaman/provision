package execution

import (
	"strings"
	"testing"

	"provision/internal/planner"
)

func TestResolvedQueueSecretIsSelectedOnlyForTheTypedOperation(t *testing.T) {
	queue := planner.Operation{
		ID: "op-01", Kind: planner.PrepareQueue,
		Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Queue: &planner.AsyncQueueInput{CredentialReference: "secret://lab/rabbitmq-url"}}},
	}
	http := planner.Operation{ID: "op-02", Kind: planner.StartCandidate}
	plan := planner.Plan{Operations: []planner.Operation{queue, http}}
	secret := "amqp://queue_user:LongRandomPassword_1234@127.0.0.1:25672/"

	selected, err := resolvedValuesForOperation(plan, "op-01", map[string]string{
		"secret://lab/rabbitmq-url": secret,
		"secret://lab/unrelated":    "must-not-cross-boundary",
	})
	if err != nil || len(selected) != 1 || selected["secret://lab/rabbitmq-url"] != secret {
		t.Fatalf("resolved selection = %#v, %v", selected, err)
	}
	if selected, err := resolvedValuesForOperation(plan, "op-02", map[string]string{"secret://lab/rabbitmq-url": secret}); err != nil || len(selected) != 0 {
		t.Fatalf("non-secret operation received resolved values: %#v, %v", selected, err)
	}
	if _, err := resolvedValuesForOperation(plan, "op-01", nil); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
		t.Fatalf("missing secret was accepted or disclosed: %v", err)
	}
}
