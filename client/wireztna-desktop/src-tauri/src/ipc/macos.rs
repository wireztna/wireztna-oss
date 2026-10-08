use std::{
    collections::HashMap,
    io::{self, Read, Write},
    net::Shutdown,
    os::{
        fd::{AsRawFd, OwnedFd},
        unix::net::UnixStream,
    },
    path::Path,
    sync::{
        Arc, Condvar, Mutex, MutexGuard, Weak,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use serde_json::Value;
use socket2::{Domain, SockAddr, Socket, Type};
use tauri::{AppHandle, Emitter, State};

use super::BridgeFailure;

const DESKTOP_SOCKET_PATH: &str = "/var/run/wireztna/desktop-v2.sock";
const MAX_REQUEST_BYTES: usize = 64 * 1024;
const MAX_RESPONSE_BYTES: usize = 256 * 1024;
const MAX_PENDING_EVENT_HANDSHAKES: usize = 16;
const PENDING_EVENT_TTL: Duration = Duration::from_secs(5);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(3);
const COMMAND_IO_TIMEOUT: Duration = Duration::from_secs(35);
const EVENT_IO_TIMEOUT: Duration = Duration::from_secs(24 * 60 * 60);
const IPC_EVENT_PREFIX: &str = "wireztna://ipc-event/";
const IPC_ERROR_PREFIX: &str = "wireztna://ipc-error/";

fn frame_io_failure(error: &io::Error) -> BridgeFailure {
    match error.kind() {
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock => BridgeFailure::new("IPC_TIMEOUT"),
        io::ErrorKind::UnexpectedEof => BridgeFailure::new("IPC_TRUNCATED_FRAME"),
        io::ErrorKind::InvalidData => BridgeFailure::new("IPC_INVALID_MESSAGE"),
        _ => BridgeFailure::new("IPC_IO_FAILED"),
    }
}

fn connect_failure(error: &io::Error) -> BridgeFailure {
    match error.kind() {
        io::ErrorKind::NotFound => BridgeFailure::new("IPC_HELPER_NOT_FOUND"),
        io::ErrorKind::ConnectionRefused => BridgeFailure::new("IPC_HELPER_NOT_RUNNING"),
        io::ErrorKind::PermissionDenied => BridgeFailure::new("IPC_HELPER_PERMISSION_DENIED"),
        _ => BridgeFailure::new("IPC_CONNECT_FAILED"),
    }
}

struct BoundedJsonBuffer {
    bytes: Vec<u8>,
    limit: usize,
}

impl BoundedJsonBuffer {
    fn new(limit: usize) -> Self {
        Self {
            bytes: Vec::new(),
            limit,
        }
    }
}

impl Write for BoundedJsonBuffer {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        let next_len =
            self.bytes.len().checked_add(bytes.len()).ok_or_else(|| {
                io::Error::new(io::ErrorKind::InvalidData, "frame length overflow")
            })?;
        if next_len > self.limit {
            return Err(io::Error::new(
                io::ErrorKind::FileTooLarge,
                "frame too large",
            ));
        }
        self.bytes.extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

fn encode_json(value: &Value, limit: usize) -> Result<Vec<u8>, BridgeFailure> {
    let mut buffer = BoundedJsonBuffer::new(limit);
    match serde_json::to_writer(&mut buffer, value) {
        Ok(()) => Ok(buffer.bytes),
        Err(error) if error.io_error_kind() == Some(io::ErrorKind::FileTooLarge) => {
            Err(BridgeFailure::new("IPC_FRAME_TOO_LARGE"))
        }
        Err(_) => Err(BridgeFailure::new("IPC_INVALID_MESSAGE")),
    }
}

fn write_json_frame(writer: &mut impl Write, value: &Value) -> Result<(), BridgeFailure> {
    let payload = encode_json(value, MAX_REQUEST_BYTES)?;
    let length =
        u32::try_from(payload.len()).map_err(|_| BridgeFailure::new("IPC_FRAME_TOO_LARGE"))?;
    writer
        .write_all(&length.to_be_bytes())
        .and_then(|()| writer.write_all(&payload))
        .map_err(|error| frame_io_failure(&error))
}

fn read_json_frame(reader: &mut impl Read) -> Result<Value, BridgeFailure> {
    let mut prefix = [0_u8; 4];
    reader
        .read_exact(&mut prefix)
        .map_err(|error| frame_io_failure(&error))?;
    let wire_length = u32::from_be_bytes(prefix);
    if wire_length == 0 {
        return Err(BridgeFailure::new("IPC_INVALID_MESSAGE"));
    }
    let length =
        usize::try_from(wire_length).map_err(|_| BridgeFailure::new("IPC_FRAME_TOO_LARGE"))?;
    if length > MAX_RESPONSE_BYTES {
        return Err(BridgeFailure::new("IPC_FRAME_TOO_LARGE"));
    }

    let mut payload = vec![0_u8; length];
    reader
        .read_exact(&mut payload)
        .map_err(|error| frame_io_failure(&error))?;
    serde_json::from_slice(&payload).map_err(|_| BridgeFailure::new("IPC_INVALID_JSON"))
}

fn configure_stream(stream: &UnixStream, read_timeout: Duration) -> Result<(), BridgeFailure> {
    stream
        .set_read_timeout(Some(read_timeout))
        .and_then(|()| stream.set_write_timeout(Some(COMMAND_IO_TIMEOUT)))
        .map_err(|error| frame_io_failure(&error))
}

unsafe extern "C" {
    fn getpeereid(socket: i32, effective_uid: *mut u32, effective_gid: *mut u32) -> i32;
}

fn server_peer_uid(stream: &UnixStream) -> Result<u32, BridgeFailure> {
    let mut effective_uid = 0_u32;
    let mut effective_gid = 0_u32;
    // SAFETY: getpeereid only writes the two fixed-size credential outputs for
    // the valid connected descriptor borrowed from UnixStream.
    let result = unsafe { getpeereid(stream.as_raw_fd(), &mut effective_uid, &mut effective_gid) };
    if result != 0 {
        return Err(BridgeFailure::new("IPC_CONNECT_FAILED"));
    }
    Ok(effective_uid)
}

fn verify_server_uid(stream: &UnixStream, expected_uid: u32) -> Result<(), BridgeFailure> {
    if server_peer_uid(stream)? != expected_uid {
        return Err(BridgeFailure::new("IPC_HELPER_UNTRUSTED"));
    }
    Ok(())
}

fn connect_socket(path: &Path) -> Result<UnixStream, BridgeFailure> {
    let socket =
        Socket::new(Domain::UNIX, Type::STREAM, None).map_err(|error| connect_failure(&error))?;
    let address = SockAddr::unix(path).map_err(|error| connect_failure(&error))?;
    socket
        .connect_timeout(&address, CONNECT_TIMEOUT)
        .map_err(|error| connect_failure(&error))?;
    let descriptor: OwnedFd = socket.into();
    let stream = UnixStream::from(descriptor);
    verify_server_uid(&stream, 0)?;
    configure_stream(&stream, COMMAND_IO_TIMEOUT)
        .map_err(|_| BridgeFailure::new("IPC_CONNECT_FAILED"))?;
    Ok(stream)
}

fn close_stream(stream: UnixStream) {
    let _ = stream.shutdown(Shutdown::Both);
}

fn object_string<'a>(value: &'a Value, field: &str) -> Result<&'a str, BridgeFailure> {
    value
        .as_object()
        .and_then(|object| object.get(field))
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| BridgeFailure::new("IPC_INVALID_ARGUMENT"))
}

fn valid_request_id(request_id: &str) -> bool {
    !request_id.is_empty()
        && request_id.len() <= 128
        && request_id
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
}

fn event_name(prefix: &str, request_id: &str) -> Result<String, BridgeFailure> {
    if !valid_request_id(request_id) {
        return Err(BridgeFailure::new("IPC_INVALID_ARGUMENT"));
    }
    Ok(format!("{prefix}{request_id}"))
}

fn successful_hello(value: &Value) -> bool {
    let Some(object) = value.as_object() else {
        return false;
    };
    object.get("kind").and_then(Value::as_str) == Some("hello")
        && object.get("negotiated_version").and_then(Value::as_u64) == Some(2)
        && object
            .get("stream_id")
            .and_then(Value::as_str)
            .is_some_and(|id| !id.is_empty())
        && object
            .get("epoch")
            .and_then(Value::as_u64)
            .is_some_and(|epoch| epoch >= 1)
        && !object.contains_key("error")
}

fn successful_response(value: &Value, request_id: &str) -> Result<bool, BridgeFailure> {
    let object = value
        .as_object()
        .ok_or_else(|| BridgeFailure::new("IPC_INVALID_MESSAGE"))?;
    if object.get("request_id").and_then(Value::as_str) != Some(request_id) {
        return Err(BridgeFailure::new("IPC_INVALID_MESSAGE"));
    }
    object
        .get("success")
        .and_then(Value::as_bool)
        .ok_or_else(|| BridgeFailure::new("IPC_INVALID_MESSAGE"))
}

struct CommandConnection {
    token: u64,
    stream: UnixStream,
}

enum PendingEvent {
    Reserved {
        token: u64,
    },
    Ready {
        token: u64,
        expires_at: Instant,
        stream: UnixStream,
    },
}

impl PendingEvent {
    fn token(&self) -> u64 {
        match self {
            Self::Reserved { token } | Self::Ready { token, .. } => *token,
        }
    }

    fn into_stream(self) -> Option<UnixStream> {
        match self {
            Self::Reserved { .. } => None,
            Self::Ready { stream, .. } => Some(stream),
        }
    }
}

#[derive(Clone)]
struct SubscriptionControl {
    token: u64,
    active: Arc<AtomicBool>,
    cancel_stream: Arc<UnixStream>,
}

#[derive(Clone, Copy, Eq, PartialEq)]
enum ShutdownPhase {
    Running,
    Draining,
    ShutDown,
}

struct Lifecycle {
    phase: ShutdownPhase,
    next_token: u64,
    command: Option<CommandConnection>,
    pending_events: HashMap<String, PendingEvent>,
    subscriptions: HashMap<String, SubscriptionControl>,
}

impl Default for Lifecycle {
    fn default() -> Self {
        Self {
            phase: ShutdownPhase::Running,
            next_token: 1,
            command: None,
            pending_events: HashMap::new(),
            subscriptions: HashMap::new(),
        }
    }
}

impl Lifecycle {
    fn allocate_token(&mut self) -> u64 {
        let token = self.next_token;
        self.next_token = self.next_token.wrapping_add(1);
        if self.next_token == 0 {
            self.next_token = 1;
        }
        token
    }
}

struct DesktopIpcInner {
    lifecycle: Mutex<Lifecycle>,
    command_io: Mutex<()>,
    shutdown_complete: Condvar,
}

impl Default for DesktopIpcInner {
    fn default() -> Self {
        Self {
            lifecycle: Mutex::new(Lifecycle::default()),
            command_io: Mutex::new(()),
            shutdown_complete: Condvar::new(),
        }
    }
}

impl DesktopIpcInner {
    fn lock_for_shutdown(&self) -> MutexGuard<'_, Lifecycle> {
        self.lifecycle
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }

    fn shutdown(&self) {
        let (command, pending, subscriptions) = {
            let mut lifecycle = self.lock_for_shutdown();
            loop {
                match lifecycle.phase {
                    ShutdownPhase::ShutDown => return,
                    ShutdownPhase::Draining => {
                        lifecycle = self
                            .shutdown_complete
                            .wait(lifecycle)
                            .unwrap_or_else(std::sync::PoisonError::into_inner);
                    }
                    ShutdownPhase::Running => break,
                }
            }

            lifecycle.phase = ShutdownPhase::Draining;
            let command = lifecycle.command.take().map(|command| command.stream);
            let pending = lifecycle
                .pending_events
                .drain()
                .filter_map(|(_, pending)| pending.into_stream())
                .collect::<Vec<_>>();
            let subscriptions = lifecycle
                .subscriptions
                .drain()
                .map(|(_, control)| control)
                .collect::<Vec<_>>();
            (command, pending, subscriptions)
        };

        for control in &subscriptions {
            control.active.store(false, Ordering::Release);
        }
        if let Some(command) = command {
            close_stream(command);
        }
        for stream in pending {
            close_stream(stream);
        }
        for control in subscriptions {
            let _ = control.cancel_stream.shutdown(Shutdown::Both);
        }

        let mut lifecycle = self.lock_for_shutdown();
        lifecycle.phase = ShutdownPhase::ShutDown;
        self.shutdown_complete.notify_all();
    }
}

impl Drop for DesktopIpcInner {
    fn drop(&mut self) {
        self.shutdown();
    }
}

#[derive(Clone)]
pub(crate) struct DesktopIpcState {
    inner: Arc<DesktopIpcInner>,
}

impl Default for DesktopIpcState {
    fn default() -> Self {
        let inner = Arc::new(DesktopIpcInner::default());
        let weak = Arc::downgrade(&inner);
        thread::spawn(move || {
            loop {
                thread::sleep(Duration::from_millis(100));
                let Some(inner) = weak.upgrade() else {
                    return;
                };
                let due = {
                    let lifecycle = inner
                        .lifecycle
                        .lock()
                        .unwrap_or_else(std::sync::PoisonError::into_inner);
                    if lifecycle.phase != ShutdownPhase::Running {
                        return;
                    }
                    let now = Instant::now();
                    lifecycle
                        .pending_events
                        .iter()
                        .filter_map(|(request_id, pending)| match pending {
                            PendingEvent::Ready {
                                token, expires_at, ..
                            } if *expires_at <= now => Some((request_id.clone(), *token)),
                            _ => None,
                        })
                        .collect::<Vec<_>>()
                };
                let state = Self { inner };
                for (request_id, token) in due {
                    state.expire_pending(&request_id, token);
                }
            }
        });
        Self { inner }
    }
}

impl DesktopIpcState {
    fn lifecycle(&self) -> Result<MutexGuard<'_, Lifecycle>, BridgeFailure> {
        self.inner
            .lifecycle
            .lock()
            .map_err(|_| BridgeFailure::new("IPC_INTERNAL"))
    }

    fn ensure_running(&self) -> Result<(), BridgeFailure> {
        if self.lifecycle()?.phase == ShutdownPhase::Running {
            Ok(())
        } else {
            Err(BridgeFailure::new("IPC_SHUTDOWN"))
        }
    }

    fn normalize_operation_error(&self, error: BridgeFailure) -> BridgeFailure {
        match self.inner.lifecycle.lock() {
            Ok(lifecycle) if lifecycle.phase != ShutdownPhase::Running => {
                BridgeFailure::new("IPC_SHUTDOWN")
            }
            _ => error,
        }
    }

    fn reserve_pending(&self, request_id: String) -> Result<u64, BridgeFailure> {
        let mut lifecycle = self.lifecycle()?;
        if lifecycle.phase != ShutdownPhase::Running {
            return Err(BridgeFailure::new("IPC_SHUTDOWN"));
        }
        if lifecycle.pending_events.contains_key(&request_id) {
            return Err(BridgeFailure::new("IPC_REQUEST_IN_PROGRESS"));
        }
        if lifecycle.pending_events.len() >= MAX_PENDING_EVENT_HANDSHAKES {
            return Err(BridgeFailure::new("IPC_PENDING_LIMIT"));
        }
        let token = lifecycle.allocate_token();
        lifecycle
            .pending_events
            .insert(request_id, PendingEvent::Reserved { token });
        Ok(token)
    }

    fn remove_pending_if_token(&self, request_id: &str, token: u64) -> Option<UnixStream> {
        let pending = {
            let mut lifecycle = self
                .inner
                .lifecycle
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner);
            if lifecycle
                .pending_events
                .get(request_id)
                .is_some_and(|pending| pending.token() == token)
            {
                lifecycle.pending_events.remove(request_id)
            } else {
                None
            }
        };
        pending.and_then(PendingEvent::into_stream)
    }

    fn commit_pending(
        &self,
        request_id: String,
        token: u64,
        stream: UnixStream,
    ) -> Result<(), BridgeFailure> {
        let mut stream = Some(stream);
        let committed = {
            let mut lifecycle = self.lifecycle()?;
            let reserved = lifecycle.phase == ShutdownPhase::Running
                && lifecycle
                    .pending_events
                    .get(&request_id)
                    .is_some_and(|pending| {
                        matches!(pending, PendingEvent::Reserved { token: current } if *current == token)
                    });
            if reserved {
                lifecycle.pending_events.insert(
                    request_id,
                    PendingEvent::Ready {
                        token,
                        expires_at: Instant::now() + PENDING_EVENT_TTL,
                        stream: stream.take().expect("stream is available"),
                    },
                );
            }
            reserved
        };

        if let Some(stream) = stream {
            close_stream(stream);
        }
        if !committed {
            return Err(self.normalize_operation_error(BridgeFailure::new("IPC_CANCELED")));
        }
        Ok(())
    }

    fn expire_pending(&self, request_id: &str, token: u64) -> bool {
        if let Some(stream) = self.remove_pending_if_token(request_id, token) {
            close_stream(stream);
            true
        } else {
            false
        }
    }

    fn replace_command(&self, replacement: UnixStream) -> Result<(), BridgeFailure> {
        let _command_guard = self
            .inner
            .command_io
            .lock()
            .map_err(|_| BridgeFailure::new("IPC_INTERNAL"))?;
        let mut replacement = Some(replacement);
        let previous = {
            let mut lifecycle = self.lifecycle()?;
            if lifecycle.phase != ShutdownPhase::Running {
                None
            } else {
                let token = lifecycle.allocate_token();
                lifecycle.command.replace(CommandConnection {
                    token,
                    stream: replacement.take().expect("stream is available"),
                })
            }
        };
        if let Some(replacement) = replacement {
            close_stream(replacement);
            return Err(BridgeFailure::new("IPC_SHUTDOWN"));
        }
        if let Some(previous) = previous {
            close_stream(previous.stream);
        }
        Ok(())
    }

    fn handshake_with(
        &self,
        hello: Value,
        request_id: Option<String>,
        connect: impl FnOnce() -> Result<UnixStream, BridgeFailure>,
    ) -> Result<Value, BridgeFailure> {
        let channel = object_string(&hello, "channel")?;
        if !matches!(channel, "command" | "event") {
            return Err(BridgeFailure::new("IPC_INVALID_ARGUMENT"));
        }
        let event_request = if channel == "event" {
            let request_id =
                request_id.ok_or_else(|| BridgeFailure::new("IPC_INVALID_ARGUMENT"))?;
            if !valid_request_id(&request_id) {
                return Err(BridgeFailure::new("IPC_INVALID_ARGUMENT"));
            }
            let token = self.reserve_pending(request_id.clone())?;
            Some((request_id, token))
        } else {
            if request_id.is_some() {
                return Err(BridgeFailure::new("IPC_INVALID_ARGUMENT"));
            }
            self.ensure_running()?;
            None
        };

        let mut stream = match connect() {
            Ok(stream) => stream,
            Err(error) => {
                if let Some((request_id, token)) = &event_request {
                    self.remove_pending_if_token(request_id, *token);
                }
                return Err(self.normalize_operation_error(error));
            }
        };
        if let Err(error) = write_json_frame(&mut stream, &hello) {
            close_stream(stream);
            if let Some((request_id, token)) = &event_request {
                self.remove_pending_if_token(request_id, *token);
            }
            return Err(self.normalize_operation_error(error));
        }
        let response = match read_json_frame(&mut stream) {
            Ok(response) => response,
            Err(error) => {
                close_stream(stream);
                if let Some((request_id, token)) = &event_request {
                    self.remove_pending_if_token(request_id, *token);
                }
                return Err(self.normalize_operation_error(error));
            }
        };
        if !successful_hello(&response) {
            close_stream(stream);
            if let Some((request_id, token)) = &event_request {
                self.remove_pending_if_token(request_id, *token);
            }
            return Ok(response);
        }

        match event_request {
            None => self.replace_command(stream)?,
            Some((request_id, token)) => {
                self.commit_pending(request_id, token, stream)?;
            }
        }
        Ok(response)
    }

    fn handshake(&self, hello: Value, request_id: Option<String>) -> Result<Value, BridgeFailure> {
        self.handshake_with(hello, request_id, || {
            connect_socket(Path::new(DESKTOP_SOCKET_PATH))
        })
    }

    fn command_request(&self, request: &Value) -> Result<Value, BridgeFailure> {
        let _command_guard = self
            .inner
            .command_io
            .lock()
            .map_err(|_| BridgeFailure::new("IPC_INTERNAL"))?;
        let (token, mut stream) = {
            let lifecycle = self.lifecycle()?;
            if lifecycle.phase != ShutdownPhase::Running {
                return Err(BridgeFailure::new("IPC_SHUTDOWN"));
            }
            let command = lifecycle
                .command
                .as_ref()
                .ok_or_else(|| BridgeFailure::new("IPC_CHANNEL_NOT_NEGOTIATED"))?;
            let stream = command
                .stream
                .try_clone()
                .map_err(|error| frame_io_failure(&error))?;
            (command.token, stream)
        };

        let result =
            write_json_frame(&mut stream, request).and_then(|()| read_json_frame(&mut stream));
        if let Err(error) = result {
            let failed = {
                let mut lifecycle = self.lifecycle()?;
                if lifecycle
                    .command
                    .as_ref()
                    .is_some_and(|command| command.token == token)
                {
                    lifecycle.command.take()
                } else {
                    None
                }
            };
            if let Some(failed) = failed {
                close_stream(failed.stream);
            }
            return Err(self.normalize_operation_error(error));
        }
        self.ensure_running()?;
        result
    }

    fn remove_subscription_if_token(
        &self,
        request_id: &str,
        token: u64,
    ) -> Option<SubscriptionControl> {
        let mut lifecycle = self
            .inner
            .lifecycle
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if lifecycle
            .subscriptions
            .get(request_id)
            .is_some_and(|control| control.token == token)
        {
            lifecycle.subscriptions.remove(request_id)
        } else {
            None
        }
    }

    fn stop_subscription(&self, request_id: &str, token: u64) {
        if let Some(control) = self.remove_subscription_if_token(request_id, token) {
            control.active.store(false, Ordering::Release);
            let _ = control.cancel_stream.shutdown(Shutdown::Both);
        }
    }

    fn subscribe_request(
        &self,
        app: AppHandle,
        request: Value,
        request_id: String,
    ) -> Result<Value, BridgeFailure> {
        let (mut stream, control, previous) = {
            let mut lifecycle = self.lifecycle()?;
            if lifecycle.phase != ShutdownPhase::Running {
                return Err(BridgeFailure::new("IPC_SHUTDOWN"));
            }
            let cancel_stream = match lifecycle.pending_events.get(&request_id) {
                Some(PendingEvent::Ready { stream, .. }) => stream
                    .try_clone()
                    .map_err(|error| frame_io_failure(&error))?,
                _ => return Err(BridgeFailure::new("IPC_CHANNEL_NOT_NEGOTIATED")),
            };
            let pending = lifecycle
                .pending_events
                .remove(&request_id)
                .expect("ready pending event exists");
            let (token, stream) = match pending {
                PendingEvent::Ready { token, stream, .. } => (token, stream),
                PendingEvent::Reserved { .. } => unreachable!("checked ready pending event"),
            };
            let control = SubscriptionControl {
                token,
                active: Arc::new(AtomicBool::new(true)),
                cancel_stream: Arc::new(cancel_stream),
            };
            let previous = lifecycle
                .subscriptions
                .insert(request_id.clone(), control.clone());
            (stream, control, previous)
        };

        if let Some(previous) = previous {
            previous.active.store(false, Ordering::Release);
            let _ = previous.cancel_stream.shutdown(Shutdown::Both);
        }

        let response = match write_json_frame(&mut stream, &request)
            .and_then(|()| read_json_frame(&mut stream))
        {
            Ok(response) => response,
            Err(error) => {
                self.stop_subscription(&request_id, control.token);
                close_stream(stream);
                return Err(self.normalize_operation_error(error));
            }
        };
        let successful = match successful_response(&response, &request_id) {
            Ok(successful) => successful,
            Err(error) => {
                self.stop_subscription(&request_id, control.token);
                close_stream(stream);
                return Err(error);
            }
        };
        if !successful {
            self.stop_subscription(&request_id, control.token);
            close_stream(stream);
            return Ok(response);
        }
        if let Err(error) = configure_stream(&stream, EVENT_IO_TIMEOUT) {
            self.stop_subscription(&request_id, control.token);
            close_stream(stream);
            return Err(error);
        }

        let can_start = {
            let lifecycle = self.lifecycle()?;
            lifecycle.phase == ShutdownPhase::Running
                && control.active.load(Ordering::Acquire)
                && lifecycle
                    .subscriptions
                    .get(&request_id)
                    .is_some_and(|current| current.token == control.token)
        };
        if !can_start {
            self.stop_subscription(&request_id, control.token);
            close_stream(stream);
            return Err(self.normalize_operation_error(BridgeFailure::new("IPC_CANCELED")));
        }

        let inner = Arc::downgrade(&self.inner);
        let active = control.active.clone();
        let token = control.token;
        tauri::async_runtime::spawn_blocking(move || {
            pump_events(inner, stream, request_id, token, active, |name, payload| {
                app.emit(name, payload)
                    .map_err(|_| BridgeFailure::new("IPC_EVENT_DELIVERY_FAILED"))
            });
        });
        Ok(response)
    }

    fn request(&self, app: AppHandle, request: Value) -> Result<Value, BridgeFailure> {
        let command = object_string(&request, "command")?;
        if command == "subscribe" {
            let request_id = object_string(&request, "request_id")?.to_owned();
            event_name(IPC_EVENT_PREFIX, &request_id)?;
            self.subscribe_request(app, request, request_id)
        } else if matches!(
            command,
            "connect" | "disconnect" | "switch" | "get_snapshot"
        ) {
            self.command_request(&request)
        } else {
            Err(BridgeFailure::new("IPC_INVALID_ARGUMENT"))
        }
    }

    fn cancel_subscription(&self, request_id: &str) -> Result<(), BridgeFailure> {
        if !valid_request_id(request_id) {
            return Err(BridgeFailure::new("IPC_INVALID_ARGUMENT"));
        }
        let (pending, control) = {
            let mut lifecycle = self.lifecycle()?;
            (
                lifecycle.pending_events.remove(request_id),
                lifecycle.subscriptions.remove(request_id),
            )
        };
        if let Some(stream) = pending.and_then(PendingEvent::into_stream) {
            close_stream(stream);
        }
        if let Some(control) = control {
            control.active.store(false, Ordering::Release);
            let _ = control.cancel_stream.shutdown(Shutdown::Both);
        }
        Ok(())
    }

    pub(crate) fn shutdown(&self) {
        self.inner.shutdown();
    }
}

fn pump_events(
    inner: Weak<DesktopIpcInner>,
    mut stream: UnixStream,
    request_id: String,
    token: u64,
    active: Arc<AtomicBool>,
    mut emit: impl FnMut(&str, Value) -> Result<(), BridgeFailure>,
) {
    let scoped_event_name =
        event_name(IPC_EVENT_PREFIX, &request_id).expect("validated request id");
    let scoped_error_name =
        event_name(IPC_ERROR_PREFIX, &request_id).expect("validated request id");

    loop {
        let frame = read_json_frame(&mut stream);
        let Some(owner) = inner.upgrade() else {
            break;
        };
        let lifecycle = owner
            .lifecycle
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let current = lifecycle.phase == ShutdownPhase::Running
            && active.load(Ordering::Acquire)
            && lifecycle
                .subscriptions
                .get(&request_id)
                .is_some_and(|control| control.token == token);
        if !current {
            break;
        }
        let continue_pumping = match frame {
            Ok(payload) => emit(&scoped_event_name, payload).is_ok(),
            Err(error) => {
                let payload = serde_json::to_value(error)
                    .unwrap_or_else(|_| serde_json::json!({ "code": "IPC_INTERNAL" }));
                let _ = emit(&scoped_error_name, payload);
                false
            }
        };
        drop(lifecycle);
        if !continue_pumping {
            break;
        }
    }

    active.store(false, Ordering::Release);
    let _ = stream.shutdown(Shutdown::Both);
    if let Some(owner) = inner.upgrade() {
        DesktopIpcState { inner: owner }.remove_subscription_if_token(&request_id, token);
    }
}

async fn blocking<T: Send + 'static>(
    operation: impl FnOnce() -> Result<T, BridgeFailure> + Send + 'static,
) -> Result<T, BridgeFailure> {
    tauri::async_runtime::spawn_blocking(operation)
        .await
        .map_err(|_| BridgeFailure::new("IPC_INTERNAL"))?
}

#[tauri::command]
pub(crate) async fn desktop_ipc_handshake(
    state: State<'_, DesktopIpcState>,
    hello: Value,
    request_id: Option<String>,
) -> Result<Value, BridgeFailure> {
    let state = state.inner().clone();
    blocking(move || state.handshake(hello, request_id)).await
}

#[tauri::command]
pub(crate) async fn desktop_ipc_request(
    app: AppHandle,
    state: State<'_, DesktopIpcState>,
    request: Value,
) -> Result<Value, BridgeFailure> {
    let state = state.inner().clone();
    blocking(move || state.request(app, request)).await
}

#[tauri::command]
pub(crate) async fn desktop_ipc_cancel_subscription(
    state: State<'_, DesktopIpcState>,
    request_id: String,
) -> Result<(), BridgeFailure> {
    let state = state.inner().clone();
    blocking(move || state.cancel_subscription(&request_id)).await
}

#[cfg(test)]
mod tests {
    use std::{
        io::{Cursor, Read, Write},
        os::unix::net::UnixStream,
        sync::{Arc, Mutex, atomic::AtomicBool},
        thread,
    };

    use serde_json::{Value, json};

    use super::{
        BridgeFailure, CommandConnection, DesktopIpcState, IPC_ERROR_PREFIX, IPC_EVENT_PREFIX,
        Instant, MAX_PENDING_EVENT_HANDSHAKES, MAX_REQUEST_BYTES, MAX_RESPONSE_BYTES,
        PENDING_EVENT_TTL, PendingEvent, ShutdownPhase, SubscriptionControl, close_stream,
        connect_failure, event_name, pump_events, read_json_frame, server_peer_uid,
        verify_server_uid, write_json_frame,
    };

    fn framed(value: &Value) -> Vec<u8> {
        let payload = serde_json::to_vec(value).unwrap();
        let mut bytes = Vec::new();
        bytes.extend_from_slice(&(payload.len() as u32).to_be_bytes());
        bytes.extend_from_slice(&payload);
        bytes
    }

    fn hello_response() -> Value {
        json!({
            "kind": "hello",
            "negotiated_version": 2,
            "service_version": "test",
            "capabilities": [],
            "compatibility": "current",
            "stream_id": "stream",
            "epoch": 1
        })
    }

    fn assert_peer_closed(stream: &mut UnixStream) {
        stream.set_nonblocking(true).unwrap();
        let mut byte = [0_u8; 1];
        assert_eq!(stream.read(&mut byte).unwrap(), 0);
    }

    #[test]
    fn framing_is_uint32_big_endian_and_handles_fragmented_reads() {
        let payload = json!({ "bytes": "x".repeat(244) });
        let mut encoded = Vec::new();
        write_json_frame(&mut encoded, &payload).unwrap();
        assert_eq!(&encoded[..4], &[0, 0, 1, 0]);

        struct Fragmented(Cursor<Vec<u8>>);
        impl Read for Fragmented {
            fn read(&mut self, target: &mut [u8]) -> std::io::Result<usize> {
                let chunk_len = target.len().min(2);
                self.0.read(&mut target[..chunk_len])
            }
        }
        assert_eq!(
            read_json_frame(&mut Fragmented(Cursor::new(encoded))).unwrap(),
            payload
        );
    }

    #[test]
    fn framing_enforces_both_limits_and_rejects_before_body_read() {
        let exact_request = json!({ "value": "x".repeat(MAX_REQUEST_BYTES - 12) });
        let mut output = Vec::new();
        write_json_frame(&mut output, &exact_request).unwrap();
        assert_eq!(output.len(), MAX_REQUEST_BYTES + 4);

        let oversized_request = json!({ "value": "x".repeat(MAX_REQUEST_BYTES - 11) });
        assert_eq!(
            write_json_frame(&mut Vec::new(), &oversized_request).unwrap_err(),
            BridgeFailure::new("IPC_FRAME_TOO_LARGE")
        );

        struct PrefixOnly {
            prefix: Cursor<[u8; 4]>,
            body_read: bool,
        }
        impl Read for PrefixOnly {
            fn read(&mut self, target: &mut [u8]) -> std::io::Result<usize> {
                if self.prefix.position() < 4 {
                    return self.prefix.read(target);
                }
                self.body_read = true;
                panic!("oversized body must not be read or allocated");
            }
        }
        let mut source = PrefixOnly {
            prefix: Cursor::new(((MAX_RESPONSE_BYTES + 1) as u32).to_be_bytes()),
            body_read: false,
        };
        assert_eq!(
            read_json_frame(&mut source).unwrap_err(),
            BridgeFailure::new("IPC_FRAME_TOO_LARGE")
        );
        assert!(!source.body_read);
    }

    #[test]
    fn framing_rejects_empty_truncated_and_invalid_json_frames() {
        assert_eq!(
            read_json_frame(&mut Cursor::new([0, 0, 0, 0])).unwrap_err(),
            BridgeFailure::new("IPC_INVALID_MESSAGE")
        );
        assert_eq!(
            read_json_frame(&mut Cursor::new([0, 0])).unwrap_err(),
            BridgeFailure::new("IPC_TRUNCATED_FRAME")
        );
        assert_eq!(
            read_json_frame(&mut Cursor::new([0, 0, 0, 2, b'{'])).unwrap_err(),
            BridgeFailure::new("IPC_TRUNCATED_FRAME")
        );
        assert_eq!(
            read_json_frame(&mut Cursor::new([0, 0, 0, 1, b'{'])).unwrap_err(),
            BridgeFailure::new("IPC_INVALID_JSON")
        );
    }

    #[test]
    fn pending_handshake_cap_and_duplicate_ids_reject_before_connecting() {
        let state = DesktopIpcState::default();
        for index in 0..MAX_PENDING_EVENT_HANDSHAKES {
            state.reserve_pending(format!("request-{index}")).unwrap();
        }
        assert!(MAX_PENDING_EVENT_HANDSHAKES < 64);

        for (request_id, code) in [
            ("overflow", "IPC_PENDING_LIMIT"),
            ("request-0", "IPC_REQUEST_IN_PROGRESS"),
        ] {
            let connected = Arc::new(AtomicBool::new(false));
            let connected_by_call = connected.clone();
            let error = state
                .handshake_with(
                    json!({ "channel": "event" }),
                    Some(request_id.to_owned()),
                    move || {
                        connected_by_call.store(true, std::sync::atomic::Ordering::Release);
                        UnixStream::pair()
                            .map(|pair| pair.0)
                            .map_err(|_| BridgeFailure::new("test"))
                    },
                )
                .unwrap_err();
            assert_eq!(error, BridgeFailure::new(code));
            assert!(!connected.load(std::sync::atomic::Ordering::Acquire));
        }
    }

    #[test]
    fn pending_expiry_is_token_safe_when_request_id_is_reused() {
        let state = DesktopIpcState::default();
        let first_token = state.reserve_pending("same-request".to_owned()).unwrap();
        let (first, mut first_peer) = UnixStream::pair().unwrap();
        state
            .commit_pending("same-request".to_owned(), first_token, first)
            .unwrap();

        close_stream(
            state
                .remove_pending_if_token("same-request", first_token)
                .unwrap(),
        );
        assert_peer_closed(&mut first_peer);
        let second_token = state.reserve_pending("same-request".to_owned()).unwrap();
        let (second, mut second_peer) = UnixStream::pair().unwrap();
        state
            .commit_pending("same-request".to_owned(), second_token, second)
            .unwrap();

        assert!(!state.expire_pending("same-request", first_token));
        assert!(
            state
                .inner
                .lifecycle
                .lock()
                .unwrap()
                .pending_events
                .contains_key("same-request")
        );
        assert!(state.expire_pending("same-request", second_token));
        assert_peer_closed(&mut second_peer);
    }

    #[test]
    fn shutdown_is_idempotent_and_drains_all_socket_classes_without_late_errors() {
        let state = DesktopIpcState::default();
        let (command, mut command_peer) = UnixStream::pair().unwrap();
        let (pending, mut pending_peer) = UnixStream::pair().unwrap();
        let (active_stream, mut active_peer) = UnixStream::pair().unwrap();
        let active = Arc::new(AtomicBool::new(true));
        let emitted = Arc::new(Mutex::new(Vec::<String>::new()));
        let token = 3;
        {
            let mut lifecycle = state.inner.lifecycle.lock().unwrap();
            lifecycle.command = Some(CommandConnection {
                token: 1,
                stream: command,
            });
            lifecycle.pending_events.insert(
                "pending".to_owned(),
                PendingEvent::Ready {
                    token: 2,
                    expires_at: Instant::now() + PENDING_EVENT_TTL,
                    stream: pending,
                },
            );
            lifecycle.subscriptions.insert(
                "active".to_owned(),
                SubscriptionControl {
                    token,
                    active: active.clone(),
                    cancel_stream: Arc::new(active_stream.try_clone().unwrap()),
                },
            );
        }

        let weak = Arc::downgrade(&state.inner);
        let emitted_by_pump = emitted.clone();
        let active_by_pump = active.clone();
        let pump = thread::spawn(move || {
            pump_events(
                weak,
                active_stream,
                "active".to_owned(),
                token,
                active_by_pump,
                |name, _| {
                    emitted_by_pump.lock().unwrap().push(name.to_owned());
                    Ok(())
                },
            );
        });

        state.shutdown();
        state.shutdown();
        pump.join().unwrap();
        assert_peer_closed(&mut command_peer);
        assert_peer_closed(&mut pending_peer);
        assert_peer_closed(&mut active_peer);
        assert!(emitted.lock().unwrap().is_empty());
        let lifecycle = state.inner.lifecycle.lock().unwrap();
        assert!(lifecycle.phase == ShutdownPhase::ShutDown);
        assert!(lifecycle.command.is_none());
        assert!(lifecycle.pending_events.is_empty());
        assert!(lifecycle.subscriptions.is_empty());
    }

    #[test]
    fn connect_errors_have_stable_public_categories() {
        for (kind, code) in [
            (std::io::ErrorKind::NotFound, "IPC_HELPER_NOT_FOUND"),
            (
                std::io::ErrorKind::ConnectionRefused,
                "IPC_HELPER_NOT_RUNNING",
            ),
            (
                std::io::ErrorKind::PermissionDenied,
                "IPC_HELPER_PERMISSION_DENIED",
            ),
            (std::io::ErrorKind::TimedOut, "IPC_CONNECT_FAILED"),
            (std::io::ErrorKind::Other, "IPC_CONNECT_FAILED"),
        ] {
            assert_eq!(
                connect_failure(&std::io::Error::from(kind)),
                BridgeFailure::new(code)
            );
        }
    }

    #[test]
    fn server_credentials_are_verified_before_protocol_handshake() {
        let (client, _server) = UnixStream::pair().unwrap();
        let actual_uid = server_peer_uid(&client).unwrap();
        verify_server_uid(&client, actual_uid).unwrap();
        if actual_uid != 0 {
            assert_eq!(
                verify_server_uid(&client, 0).unwrap_err(),
                BridgeFailure::new("IPC_HELPER_UNTRUSTED")
            );
        }
    }

    #[test]
    fn handshake_keeps_command_and_event_connections_separate() {
        let state = DesktopIpcState::default();
        for channel in ["command", "event"] {
            let (client, mut service) = UnixStream::pair().unwrap();
            let channel_owned = channel.to_owned();
            let service_thread = thread::spawn(move || {
                let hello = read_json_frame(&mut service).unwrap();
                assert_eq!(hello["channel"], channel_owned);
                write_json_frame(&mut service, &hello_response()).unwrap();
            });
            let request_id = (channel == "event").then(|| "event-request".to_owned());
            state
                .handshake_with(json!({ "channel": channel }), request_id, || Ok(client))
                .unwrap();
            service_thread.join().unwrap();
        }
        let lifecycle = state.inner.lifecycle.lock().unwrap();
        assert!(lifecycle.command.is_some());
        assert!(lifecycle.pending_events.contains_key("event-request"));
    }

    #[test]
    fn event_pumps_remain_scoped() {
        let state = DesktopIpcState::default();
        let (reader_a, mut writer_a) = UnixStream::pair().unwrap();
        let (reader_b, mut writer_b) = UnixStream::pair().unwrap();
        let emitted = Arc::new(Mutex::new(Vec::<(String, Value)>::new()));
        let mut pumps = Vec::new();

        for (index, (id, reader)) in [("request-a", reader_a), ("request-b", reader_b)]
            .into_iter()
            .enumerate()
        {
            let token = index as u64 + 1;
            let active = Arc::new(AtomicBool::new(true));
            state.inner.lifecycle.lock().unwrap().subscriptions.insert(
                id.to_owned(),
                SubscriptionControl {
                    token,
                    active: active.clone(),
                    cancel_stream: Arc::new(reader.try_clone().unwrap()),
                },
            );
            let weak = Arc::downgrade(&state.inner);
            let emitted = emitted.clone();
            pumps.push(thread::spawn(move || {
                pump_events(
                    weak,
                    reader,
                    id.to_owned(),
                    token,
                    active,
                    |name, payload| {
                        emitted.lock().unwrap().push((name.to_owned(), payload));
                        Ok(())
                    },
                );
            }));
        }

        writer_a
            .write_all(&framed(&json!({ "from": "a" })))
            .unwrap();
        writer_b
            .write_all(&framed(&json!({ "from": "b" })))
            .unwrap();
        drop(writer_a);
        drop(writer_b);
        for pump in pumps {
            pump.join().unwrap();
        }

        let events = emitted.lock().unwrap();
        assert!(events.contains(&(
            event_name(IPC_EVENT_PREFIX, "request-a").unwrap(),
            json!({ "from": "a" })
        )));
        assert!(events.contains(&(
            event_name(IPC_EVENT_PREFIX, "request-b").unwrap(),
            json!({ "from": "b" })
        )));
        assert!(
            !events.iter().any(
                |(name, value)| name.ends_with("request-a") && value == &json!({ "from": "b" })
            )
        );
        assert!(
            events
                .iter()
                .any(|(name, _)| name == &event_name(IPC_ERROR_PREFIX, "request-a").unwrap())
        );
        assert!(
            events
                .iter()
                .any(|(name, _)| name == &event_name(IPC_ERROR_PREFIX, "request-b").unwrap())
        );
    }
}
