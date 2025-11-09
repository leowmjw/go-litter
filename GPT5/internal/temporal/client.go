package temporal

import (
    "context"
    "fmt"

    "go.temporal.io/sdk/client"
    "go.temporal.io/sdk/workflow"
)

type Orchestrator struct { Client client.Client; TaskQ, Name string; Parts int }

func (o *Orchestrator) wfID(p int) string { return fmt.Sprintf("rs::%s::p::%d", o.Name, p) }

func (o *Orchestrator) EnsureStarted(ctx context.Context, p int) error {
    id := o.wfID(p)
    _, err := o.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: o.TaskQ}, PartitionWorkflowV2, wfState{})
    if err != nil && !client.IsWorkflowExecutionAlreadyStartedError(err) {
        return err
    }
    return nil
}

func (o *Orchestrator) Update(ctx context.Context, routeKey, update string, payload any) error {
    p := int(routeKey[0]) % o.Parts
    if err := o.EnsureStarted(ctx, p); err != nil { return err }
    _, err := o.Client.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{WorkflowID: o.wfID(p), UpdateName: update}, payload)
    return err
}
