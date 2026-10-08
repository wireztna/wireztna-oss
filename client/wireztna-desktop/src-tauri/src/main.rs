#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod ipc;

use ipc::{
    DesktopIpcState, desktop_ipc_cancel_subscription, desktop_ipc_handshake, desktop_ipc_request,
};
use serde::{Deserialize, Serialize};
#[cfg(target_os = "macos")]
use std::{
    env, fs,
    io::Write,
    path::Path,
    process::{Command as ProcessCommand, Stdio},
};
use tauri::{
    Emitter, Manager, WindowEvent,
    image::Image,
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
};

const TRAY_ID: &str = "wireztna-status";

const SHELL_SHOWN_EVENT: &str = "wireztna://shell-shown";
const SHELL_HIDDEN_EVENT: &str = "wireztna://shell-hidden";
const SHELL_QUITTING_EVENT: &str = "wireztna://shell-quitting";

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum ShellAction {
    Show,
    Hide,
    Quit,
}

fn tray_action(id: &str) -> Option<ShellAction> {
    match id {
        "show" => Some(ShellAction::Show),
        "hide" => Some(ShellAction::Hide),
        "quit_ui" => Some(ShellAction::Quit),
        _ => None,
    }
}

fn close_action() -> ShellAction {
    ShellAction::Hide
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq)]
#[serde(rename_all = "snake_case")]
enum TrayVisualState {
    Disconnected,
    Connected,
    Attention,
}

fn tray_color(state: TrayVisualState) -> [u8; 4] {
    match state {
        TrayVisualState::Disconnected => [142, 142, 147, 255],
        TrayVisualState::Connected => [52, 199, 89, 255],
        TrayVisualState::Attention => [255, 149, 0, 255],
    }
}

fn tray_dark_color(state: TrayVisualState) -> [u8; 4] {
    match state {
        TrayVisualState::Disconnected => [99, 99, 102, 255],
        TrayVisualState::Connected => [36, 140, 60, 255],
        TrayVisualState::Attention => [201, 111, 0, 255],
    }
}

fn tray_rgba(state: TrayVisualState) -> Vec<u8> {
    const SIZE: u32 = 22;
    let mut rgba = vec![0_u8; (SIZE * SIZE * 4) as usize];
    let fill = tray_color(state);
    let dark = tray_dark_color(state);
    let center_x = f64::from(SIZE) / 2.0;
    let top_y = f64::from(SIZE) * 0.08;
    let shoulder_y = f64::from(SIZE) * 0.30;
    let waist_y = f64::from(SIZE) * 0.65;
    let bottom_y = f64::from(SIZE) * 0.92;
    let max_half_width = f64::from(SIZE) * 0.42;

    for y in 0..SIZE {
        let vertical = f64::from(y);
        let half_width = if vertical < top_y {
            0.0
        } else if vertical < shoulder_y {
            let progress = (vertical - top_y) / (shoulder_y - top_y);
            max_half_width * (1.0 - (1.0 - progress) * (1.0 - progress))
        } else if vertical < waist_y {
            max_half_width
        } else if vertical < bottom_y {
            let progress = (vertical - waist_y) / (bottom_y - waist_y);
            max_half_width * (1.0 - progress * progress)
        } else {
            0.0
        };

        for x in 0..SIZE {
            let distance = (f64::from(x) - center_x).abs();
            if half_width <= 0.5 || distance > half_width + 0.5 {
                continue;
            }
            let index = ((y * SIZE + x) * 4) as usize;
            if distance > half_width - 1.0 {
                let alpha = if distance > half_width - 0.5 {
                    ((half_width + 0.5 - distance) * 255.0).clamp(0.0, 255.0) as u8
                } else {
                    255
                };
                rgba[index..index + 4].copy_from_slice(&[dark[0], dark[1], dark[2], alpha]);
            } else {
                let progress = (vertical - top_y) / (bottom_y - top_y);
                let color = if progress < 0.4 {
                    [
                        fill[0].saturating_add(20),
                        fill[1].saturating_add(20),
                        fill[2].saturating_add(20),
                        255,
                    ]
                } else {
                    fill
                };
                rgba[index..index + 4].copy_from_slice(&color);
            }
        }
    }

    for &(x, y) in &[
        (7, 11),
        (8, 12),
        (9, 13),
        (10, 14),
        (11, 13),
        (12, 12),
        (13, 11),
        (14, 10),
        (15, 9),
    ] {
        let index = ((y * SIZE + x) * 4) as usize;
        if rgba[index + 3] != 0 {
            rgba[index..index + 4].copy_from_slice(&[255, 255, 255, 255]);
        }
    }

    rgba
}

fn tray_icon(state: TrayVisualState) -> Image<'static> {
    Image::new_owned(tray_rgba(state), 22, 22)
}

trait ShellRuntime {
    fn show_main_window(&self);
    fn hide_main_window(&self);
    fn emit_shell_event(&self, event: &'static str, source: &'static str);
    fn shutdown_ipc(&self);
    fn exit_shell(&self);
}

impl ShellRuntime for tauri::AppHandle {
    fn show_main_window(&self) {
        if let Some(window) = self.get_webview_window("main") {
            let _ = window.show();
            let _ = window.unminimize();
            let _ = window.set_focus();
        }
    }

    fn hide_main_window(&self) {
        if let Some(window) = self.get_webview_window("main") {
            let _ = window.hide();
        }
    }

    fn emit_shell_event(&self, event: &'static str, source: &'static str) {
        let _ = self.emit(event, source);
    }

    fn shutdown_ipc(&self) {
        self.state::<DesktopIpcState>().shutdown();
    }

    fn exit_shell(&self) {
        self.exit(0);
    }
}

fn apply_shell_action(runtime: &impl ShellRuntime, action: ShellAction, source: &'static str) {
    match action {
        ShellAction::Show => {
            runtime.show_main_window();
            runtime.emit_shell_event(SHELL_SHOWN_EVENT, source);
        }
        ShellAction::Hide => {
            runtime.hide_main_window();
            runtime.emit_shell_event(SHELL_HIDDEN_EVENT, source);
        }
        ShellAction::Quit => {
            // Quitting owns only the UI process. Core disconnect is intentionally
            // absent from the shell action vocabulary.
            runtime.emit_shell_event(SHELL_QUITTING_EVENT, source);
            runtime.shutdown_ipc();
            runtime.exit_shell();
        }
    }
}

#[tauri::command]
fn shell_show(app: tauri::AppHandle) {
    apply_shell_action(&app, ShellAction::Show, "api");
}

#[tauri::command]
fn shell_hide(app: tauri::AppHandle) {
    apply_shell_action(&app, ShellAction::Hide, "api");
}

#[tauri::command]
fn shell_quit(app: tauri::AppHandle) {
    apply_shell_action(&app, ShellAction::Quit, "api");
}

#[tauri::command]
fn shell_set_tray_visual_state(
    app: tauri::AppHandle,
    state: TrayVisualState,
) -> Result<(), String> {
    let tray = app
        .tray_by_id(TRAY_ID)
        .ok_or_else(|| "WireZTNA tray is unavailable".to_string())?;
    tray.set_icon(Some(tray_icon(state)))
        .map_err(|error| format!("update WireZTNA tray icon: {error}"))
}

#[cfg(target_os = "macos")]
const DESKTOP_HELPER_PATH: &str = "/Library/Application Support/WireZTNA/bin/wireztna";
#[cfg(target_os = "macos")]
const MAX_AUTH_HELPER_OUTPUT: usize = 16 * 1024;

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct DesktopAuthRequest {
    action: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    email: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    code: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct DesktopAuthResponse {
    success: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    email: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

impl DesktopAuthResponse {
    fn failure(code: &'static str) -> Self {
        Self {
            success: false,
            email: None,
            error: Some(code.to_string()),
        }
    }

    fn is_valid(&self) -> bool {
        (self.success && self.error.is_none()) || (!self.success && self.error.is_some())
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct DesktopEnrollmentRequest {
    action: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    url: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct DesktopEnrollmentResponse {
    success: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    enrolled: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    email: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

impl DesktopEnrollmentResponse {
    fn failure(code: &'static str) -> Self {
        Self {
            success: false,
            enrolled: None,
            email: None,
            error: Some(code.to_string()),
        }
    }

    fn is_valid(&self) -> bool {
        if !self.success {
            return self.error.is_some() && self.enrolled.is_none() && self.email.is_none();
        }
        if self.error.is_some() {
            return false;
        }
        match self.enrolled {
            Some(true) => self.email.as_ref().is_some_and(|email| !email.is_empty()),
            Some(false) => self.email.is_none(),
            None => false,
        }
    }
}

#[cfg(target_os = "macos")]
fn run_desktop_auth(request: DesktopAuthRequest) -> DesktopAuthResponse {
    use std::os::unix::fs::MetadataExt;

    let helper = Path::new(DESKTOP_HELPER_PATH);
    let metadata = match fs::symlink_metadata(helper) {
        Ok(metadata) => metadata,
        Err(_) => return DesktopAuthResponse::failure("AUTH_HELPER_UNAVAILABLE"),
    };
    if metadata.file_type().is_symlink()
        || !metadata.is_file()
        || metadata.uid() != 0
        || metadata.mode() & 0o022 != 0
        || metadata.mode() & 0o111 == 0
    {
        return DesktopAuthResponse::failure("AUTH_HELPER_UNTRUSTED");
    }

    let home = match env::var("HOME") {
        Ok(home) if Path::new(&home).is_absolute() => home,
        _ => return DesktopAuthResponse::failure("AUTH_CONFIG_UNAVAILABLE"),
    };
    let request_json = match serde_json::to_vec(&request) {
        Ok(request_json) => request_json,
        Err(_) => return DesktopAuthResponse::failure("AUTH_INVALID_REQUEST"),
    };

    let mut child = match ProcessCommand::new(helper)
        .arg("desktop-auth")
        .env_clear()
        .env("HOME", home)
        .env("PATH", "/usr/bin:/bin")
        .current_dir("/")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(child) => child,
        Err(_) => return DesktopAuthResponse::failure("AUTH_HELPER_UNAVAILABLE"),
    };

    let write_result = child
        .stdin
        .take()
        .ok_or(())
        .and_then(|mut input| input.write_all(&request_json).map_err(|_| ()));
    if write_result.is_err() {
        let _ = child.kill();
        let _ = child.wait();
        return DesktopAuthResponse::failure("AUTH_HELPER_FAILED");
    }

    let output = match child.wait_with_output() {
        Ok(output) if output.status.success() => output.stdout,
        _ => return DesktopAuthResponse::failure("AUTH_HELPER_FAILED"),
    };
    if output.len() > MAX_AUTH_HELPER_OUTPUT {
        return DesktopAuthResponse::failure("AUTH_HELPER_FAILED");
    }
    match serde_json::from_slice::<DesktopAuthResponse>(&output) {
        Ok(response) if response.is_valid() => response,
        _ => DesktopAuthResponse::failure("AUTH_HELPER_FAILED"),
    }
}

#[cfg(target_os = "macos")]
fn run_desktop_enrollment(request: DesktopEnrollmentRequest) -> DesktopEnrollmentResponse {
    use std::os::unix::fs::MetadataExt;

    let helper = Path::new(DESKTOP_HELPER_PATH);
    let metadata = match fs::symlink_metadata(helper) {
        Ok(metadata) => metadata,
        Err(_) => return DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_UNAVAILABLE"),
    };
    if metadata.file_type().is_symlink()
        || !metadata.is_file()
        || metadata.uid() != 0
        || metadata.mode() & 0o022 != 0
        || metadata.mode() & 0o111 == 0
    {
        return DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_UNTRUSTED");
    }

    let home = match env::var("HOME") {
        Ok(home) if Path::new(&home).is_absolute() => home,
        _ => return DesktopEnrollmentResponse::failure("ENROLLMENT_CONFIG_UNAVAILABLE"),
    };
    let request_json = match serde_json::to_vec(&request) {
        Ok(request_json) => request_json,
        Err(_) => return DesktopEnrollmentResponse::failure("ENROLLMENT_INVALID_REQUEST"),
    };
    let mut child = match ProcessCommand::new(helper)
        .arg("desktop-enroll")
        .env_clear()
        .env("HOME", home)
        .env("PATH", "/usr/bin:/bin")
        .current_dir("/")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(child) => child,
        Err(_) => return DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_UNAVAILABLE"),
    };
    if child
        .stdin
        .take()
        .ok_or(())
        .and_then(|mut input| input.write_all(&request_json).map_err(|_| ()))
        .is_err()
    {
        let _ = child.kill();
        let _ = child.wait();
        return DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_FAILED");
    }
    let output = match child.wait_with_output() {
        Ok(output) if output.status.success() => output.stdout,
        _ => return DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_FAILED"),
    };
    if output.len() > MAX_AUTH_HELPER_OUTPUT {
        return DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_FAILED");
    }
    match serde_json::from_slice::<DesktopEnrollmentResponse>(&output) {
        Ok(response) if response.is_valid() => response,
        _ => DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_FAILED"),
    }
}

#[cfg(not(target_os = "macos"))]
fn run_desktop_enrollment(_request: DesktopEnrollmentRequest) -> DesktopEnrollmentResponse {
    DesktopEnrollmentResponse::failure("ENROLLMENT_UNSUPPORTED_PLATFORM")
}

#[tauri::command]
async fn desktop_enroll(request: DesktopEnrollmentRequest) -> DesktopEnrollmentResponse {
    tauri::async_runtime::spawn_blocking(move || run_desktop_enrollment(request))
        .await
        .unwrap_or_else(|_| DesktopEnrollmentResponse::failure("ENROLLMENT_HELPER_FAILED"))
}

#[cfg(not(target_os = "macos"))]
fn run_desktop_auth(_request: DesktopAuthRequest) -> DesktopAuthResponse {
    DesktopAuthResponse::failure("AUTH_UNSUPPORTED_PLATFORM")
}

#[tauri::command]
async fn desktop_auth(request: DesktopAuthRequest) -> DesktopAuthResponse {
    tauri::async_runtime::spawn_blocking(move || run_desktop_auth(request))
        .await
        .unwrap_or_else(|_| DesktopAuthResponse::failure("AUTH_HELPER_FAILED"))
}

fn main() {
    let app = tauri::Builder::default()
        .manage(DesktopIpcState::default())
        .invoke_handler(tauri::generate_handler![
            shell_show,
            shell_hide,
            shell_quit,
            shell_set_tray_visual_state,
            desktop_enroll,
            desktop_auth,
            desktop_ipc_handshake,
            desktop_ipc_request,
            desktop_ipc_cancel_subscription,
        ])
        .setup(|app| {
            let show = MenuItem::with_id(app, "show", "Show WireZTNA", true, None::<&str>)?;
            let hide = MenuItem::with_id(app, "hide", "Hide window", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit_ui", "Quit UI", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show, &hide, &quit])?;

            TrayIconBuilder::with_id(TRAY_ID)
                .tooltip("WireZTNA Desktop — IPC v2")
                .icon(tray_icon(TrayVisualState::Attention))
                .menu(&menu)
                .on_menu_event(|app, event| {
                    if let Some(action) = tray_action(event.id.as_ref()) {
                        apply_shell_action(app, action, "tray");
                    }
                })
                .build(app)?;

            if let Some(window) = app.get_webview_window("main") {
                let app_handle = app.handle().clone();
                window.on_window_event(move |event| {
                    if let WindowEvent::CloseRequested { api, .. } = event {
                        api.prevent_close();
                        apply_shell_action(&app_handle, close_action(), "window-close");
                    }
                });
            }

            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building WireZTNA desktop UI preview");

    app.run(|app_handle, event| {
        if matches!(event, tauri::RunEvent::Exit) {
            app_handle.state::<DesktopIpcState>().shutdown();
        }
    });
}

#[cfg(test)]
mod tests {
    use std::cell::RefCell;

    use super::{
        SHELL_HIDDEN_EVENT, SHELL_QUITTING_EVENT, SHELL_SHOWN_EVENT, ShellAction, ShellRuntime,
        TrayVisualState, apply_shell_action, close_action, tray_action, tray_color, tray_rgba,
    };

    #[derive(Debug, Eq, PartialEq)]
    enum Effect {
        Show,
        Hide,
        Emit(&'static str, &'static str),
        ShutdownIpc,
        Exit,
    }

    #[derive(Default)]
    struct FakeRuntime {
        effects: RefCell<Vec<Effect>>,
    }

    impl ShellRuntime for FakeRuntime {
        fn show_main_window(&self) {
            self.effects.borrow_mut().push(Effect::Show);
        }

        fn hide_main_window(&self) {
            self.effects.borrow_mut().push(Effect::Hide);
        }

        fn emit_shell_event(&self, event: &'static str, source: &'static str) {
            self.effects.borrow_mut().push(Effect::Emit(event, source));
        }

        fn shutdown_ipc(&self) {
            self.effects.borrow_mut().push(Effect::ShutdownIpc);
        }

        fn exit_shell(&self) {
            self.effects.borrow_mut().push(Effect::Exit);
        }
    }

    #[test]
    fn tray_ids_execute_coherent_native_effects() {
        let runtime = FakeRuntime::default();
        for id in ["show", "hide", "quit_ui"] {
            apply_shell_action(&runtime, tray_action(id).unwrap(), "tray");
        }

        assert_eq!(
            *runtime.effects.borrow(),
            [
                Effect::Show,
                Effect::Emit(SHELL_SHOWN_EVENT, "tray"),
                Effect::Hide,
                Effect::Emit(SHELL_HIDDEN_EVENT, "tray"),
                Effect::Emit(SHELL_QUITTING_EVENT, "tray"),
                Effect::ShutdownIpc,
                Effect::Exit,
            ]
        );
        assert_eq!(tray_action("disconnect"), None);
        assert_eq!(tray_action("unknown"), None);
    }

    #[test]
    fn tray_visual_states_share_the_shield_mask_with_distinct_colors() {
        let disconnected = tray_rgba(TrayVisualState::Disconnected);
        let connected = tray_rgba(TrayVisualState::Connected);
        let attention = tray_rgba(TrayVisualState::Attention);

        assert_ne!(disconnected, connected);
        assert_ne!(connected, attention);
        for ((grey, green), orange) in disconnected
            .chunks_exact(4)
            .zip(connected.chunks_exact(4))
            .zip(attention.chunks_exact(4))
        {
            assert_eq!(grey[3], green[3]);
            assert_eq!(green[3], orange[3]);
        }
        for (pixels, state) in [
            (&disconnected, TrayVisualState::Disconnected),
            (&connected, TrayVisualState::Connected),
            (&attention, TrayVisualState::Attention),
        ] {
            let color = tray_color(state);
            assert!(pixels.chunks_exact(4).any(|pixel| pixel == color));
            assert!(
                pixels
                    .chunks_exact(4)
                    .any(|pixel| pixel == [255, 255, 255, 255])
            );
        }
    }

    #[test]
    fn close_hides_without_exit_disconnect_or_ipc_shutdown() {
        let runtime = FakeRuntime::default();
        assert_eq!(close_action(), ShellAction::Hide);

        apply_shell_action(&runtime, close_action(), "window-close");

        assert_eq!(
            *runtime.effects.borrow(),
            [
                Effect::Hide,
                Effect::Emit(SHELL_HIDDEN_EVENT, "window-close"),
            ]
        );
    }
}
