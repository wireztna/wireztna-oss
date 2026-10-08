use serde_json::Value;
use tauri::{AppHandle, State};

use super::BridgeFailure;

#[derive(Clone, Default)]
pub(crate) struct DesktopIpcState;

impl DesktopIpcState {
    pub(crate) fn shutdown(&self) {}
}

fn unsupported<T>() -> Result<T, BridgeFailure> {
    Err(BridgeFailure::new("IPC_UNSUPPORTED_PLATFORM"))
}

#[tauri::command]
pub(crate) async fn desktop_ipc_handshake(
    _state: State<'_, DesktopIpcState>,
    _hello: Value,
    _request_id: Option<String>,
) -> Result<Value, BridgeFailure> {
    unsupported()
}

#[tauri::command]
pub(crate) async fn desktop_ipc_request(
    _app: AppHandle,
    _state: State<'_, DesktopIpcState>,
    _request: Value,
) -> Result<Value, BridgeFailure> {
    unsupported()
}

#[tauri::command]
pub(crate) async fn desktop_ipc_cancel_subscription(
    _state: State<'_, DesktopIpcState>,
    _request_id: String,
) -> Result<(), BridgeFailure> {
    unsupported()
}
