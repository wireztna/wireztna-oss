use serde::Serialize;

#[derive(Clone, Debug, Serialize, Eq, PartialEq)]
pub(crate) struct BridgeFailure {
    code: &'static str,
}

impl BridgeFailure {
    pub(crate) const fn new(code: &'static str) -> Self {
        Self { code }
    }
}

#[cfg(target_os = "macos")]
mod macos;
#[cfg(not(target_os = "macos"))]
mod unsupported;

#[cfg(target_os = "macos")]
pub(crate) use macos::{
    DesktopIpcState, desktop_ipc_cancel_subscription, desktop_ipc_handshake, desktop_ipc_request,
};
#[cfg(not(target_os = "macos"))]
pub(crate) use unsupported::{
    DesktopIpcState, desktop_ipc_cancel_subscription, desktop_ipc_handshake, desktop_ipc_request,
};
