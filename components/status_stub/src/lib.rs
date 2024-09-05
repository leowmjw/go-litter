#![allow(warnings)]
use golem_wasm_rpc::*;
#[allow(dead_code)]
mod bindings;
pub struct Api {
    rpc: WasmRpc,
}
impl Api {}
pub struct FutureGetSummaryResult {
    pub future_invoke_result: FutureInvokeResult,
}
pub struct FutureGetStatusResult {
    pub future_invoke_result: FutureInvokeResult,
}
pub struct FuturePostStatusResult {
    pub future_invoke_result: FutureInvokeResult,
}
struct Component;
impl crate::bindings::exports::components::status_stub::stub_status::Guest
for Component {
    type Api = crate::Api;
    type FutureGetSummaryResult = crate::FutureGetSummaryResult;
    type FutureGetStatusResult = crate::FutureGetStatusResult;
    type FuturePostStatusResult = crate::FuturePostStatusResult;
}
impl crate::bindings::exports::components::status_stub::stub_status::GuestFutureGetSummaryResult
for FutureGetSummaryResult {
    fn subscribe(&self) -> bindings::wasi::io::poll::Pollable {
        let pollable = self.future_invoke_result.subscribe();
        let pollable = unsafe {
            bindings::wasi::io::poll::Pollable::from_handle(pollable.take_handle())
        };
        pollable
    }
    fn get(&self) -> Option<String> {
        self.future_invoke_result
            .get()
            .map(|result| {
                let result = result
                    .expect(
                        &format!(
                            "Failed to invoke remote {}",
                            "components:status/api.{get-summary}"
                        ),
                    );
                (result
                    .tuple_element(0)
                    .expect("tuple not found")
                    .string()
                    .expect("string not found")
                    .to_string())
            })
    }
}
impl crate::bindings::exports::components::status_stub::stub_status::GuestFutureGetStatusResult
for FutureGetStatusResult {
    fn subscribe(&self) -> bindings::wasi::io::poll::Pollable {
        let pollable = self.future_invoke_result.subscribe();
        let pollable = unsafe {
            bindings::wasi::io::poll::Pollable::from_handle(pollable.take_handle())
        };
        pollable
    }
    fn get(&self) -> Option<String> {
        self.future_invoke_result
            .get()
            .map(|result| {
                let result = result
                    .expect(
                        &format!(
                            "Failed to invoke remote {}",
                            "components:status/api.{get-status}"
                        ),
                    );
                (result
                    .tuple_element(0)
                    .expect("tuple not found")
                    .string()
                    .expect("string not found")
                    .to_string())
            })
    }
}
impl crate::bindings::exports::components::status_stub::stub_status::GuestFuturePostStatusResult
for FuturePostStatusResult {
    fn subscribe(&self) -> bindings::wasi::io::poll::Pollable {
        let pollable = self.future_invoke_result.subscribe();
        let pollable = unsafe {
            bindings::wasi::io::poll::Pollable::from_handle(pollable.take_handle())
        };
        pollable
    }
    fn get(&self) -> Option<u64> {
        self.future_invoke_result
            .get()
            .map(|result| {
                let result = result
                    .expect(
                        &format!(
                            "Failed to invoke remote {}",
                            "components:status/api.{post-status}"
                        ),
                    );
                (result
                    .tuple_element(0)
                    .expect("tuple not found")
                    .u64()
                    .expect("u64 not found"))
            })
    }
}
impl crate::bindings::exports::components::status_stub::stub_status::GuestApi for Api {
    fn new(location: crate::bindings::golem::rpc::types::Uri) -> Self {
        let location = golem_wasm_rpc::Uri {
            value: location.value,
        };
        Self {
            rpc: WasmRpc::new(&location),
        }
    }
    fn blocking_get_summary(&self, status_id: u64) -> String {
        let result = self
            .rpc
            .invoke_and_await(
                "components:status/api.{get-summary}",
                &[WitValue::builder().u64(status_id)],
            )
            .expect(
                &format!(
                    "Failed to invoke-and-await remote {}",
                    "components:status/api.{get-summary}"
                ),
            );
        (result
            .tuple_element(0)
            .expect("tuple not found")
            .string()
            .expect("string not found")
            .to_string())
    }
    fn get_summary(
        &self,
        status_id: u64,
    ) -> crate::bindings::exports::components::status_stub::stub_status::FutureGetSummaryResult {
        let result = self
            .rpc
            .async_invoke_and_await(
                "components:status/api.{get-summary}",
                &[WitValue::builder().u64(status_id)],
            );
        crate::bindings::exports::components::status_stub::stub_status::FutureGetSummaryResult::new(FutureGetSummaryResult {
            future_invoke_result: result,
        })
    }
    fn blocking_get_status(&self, status_id: u64) -> String {
        let result = self
            .rpc
            .invoke_and_await(
                "components:status/api.{get-status}",
                &[WitValue::builder().u64(status_id)],
            )
            .expect(
                &format!(
                    "Failed to invoke-and-await remote {}",
                    "components:status/api.{get-status}"
                ),
            );
        (result
            .tuple_element(0)
            .expect("tuple not found")
            .string()
            .expect("string not found")
            .to_string())
    }
    fn get_status(
        &self,
        status_id: u64,
    ) -> crate::bindings::exports::components::status_stub::stub_status::FutureGetStatusResult {
        let result = self
            .rpc
            .async_invoke_and_await(
                "components:status/api.{get-status}",
                &[WitValue::builder().u64(status_id)],
            );
        crate::bindings::exports::components::status_stub::stub_status::FutureGetStatusResult::new(FutureGetStatusResult {
            future_invoke_result: result,
        })
    }
    fn blocking_post_status(&self, account_id: u64, status: String) -> u64 {
        let result = self
            .rpc
            .invoke_and_await(
                "components:status/api.{post-status}",
                &[
                    WitValue::builder().u64(account_id),
                    WitValue::builder().string(&status),
                ],
            )
            .expect(
                &format!(
                    "Failed to invoke-and-await remote {}",
                    "components:status/api.{post-status}"
                ),
            );
        (result.tuple_element(0).expect("tuple not found").u64().expect("u64 not found"))
    }
    fn post_status(
        &self,
        account_id: u64,
        status: String,
    ) -> crate::bindings::exports::components::status_stub::stub_status::FuturePostStatusResult {
        let result = self
            .rpc
            .async_invoke_and_await(
                "components:status/api.{post-status}",
                &[
                    WitValue::builder().u64(account_id),
                    WitValue::builder().string(&status),
                ],
            );
        crate::bindings::exports::components::status_stub::stub_status::FuturePostStatusResult::new(FuturePostStatusResult {
            future_invoke_result: result,
        })
    }
    fn blocking_debug_current_state(&self) -> () {
        let result = self
            .rpc
            .invoke_and_await("components:status/api.{debug-current-state}", &[])
            .expect(
                &format!(
                    "Failed to invoke-and-await remote {}",
                    "components:status/api.{debug-current-state}"
                ),
            );
        ()
    }
    fn debug_current_state(&self) -> () {
        let result = self
            .rpc
            .invoke("components:status/api.{debug-current-state}", &[])
            .expect(
                &format!(
                    "Failed to invoke remote {}",
                    "components:status/api.{debug-current-state}"
                ),
            );
        ()
    }
}
bindings::export!(Component with_types_in bindings);
