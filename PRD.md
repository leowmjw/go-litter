# PRD

## Objective

- Port over Tutorial from --> https://redplanetlabs.com/docs/~/tutorial1.html#gsc.tab=0
- Use Temporal as high level orchestrator + control plane; not for the dataflow process. Keeps track of the dataflow nodes spun out to process; detect liveness per minute
- DataFlow process will represent the Rama concept of Module
- DataFlow node that is dead; will be detected by the control plane and it will re-run the unacknowledged batch
- Can start with microbatch of 30s first; leave the near real-time of 1s in the future

