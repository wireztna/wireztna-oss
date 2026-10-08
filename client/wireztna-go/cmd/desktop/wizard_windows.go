//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jchv/go-webview2"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/pkg/version"
)

// wizardState tracks the current window state.
type wizardState struct {
	mu        sync.Mutex
	open      bool
	webview   webview2.WebView
	step      string
	email     string
	ipc       *ipc.Client
	onDone    func()
	stopPoll  chan struct{}
	closeCh   chan struct{}
}

var wizard = &wizardState{
	step: "enroll",
	ipc:  ipc.NewClient(),
}

// openWizard opens the window starting at the given step.
// step: "enroll", "login", "status"
func openWizard(startStep string, onDone func()) {
	wizard.mu.Lock()
	if wizard.open {
		// Already open — just switch to the requested step if webview is ready
		wv := wizard.webview
		wizard.mu.Unlock()
		if wv != nil {
			wv.Dispatch(func() {
				wv.Eval(fmt.Sprintf(`goToStep("%s")`, startStep))
			})
		}
		// If wv is nil, the window is still initializing — just ignore
		return
	}
	// Mark as open immediately to prevent concurrent opens
	wizard.open = true
	wizard.step = startStep
	wizard.onDone = onDone
	wizard.stopPoll = make(chan struct{})
	wizard.closeCh = make(chan struct{})
	wizard.mu.Unlock()

	// Run window on a new goroutine — blocks until window closes
	go runWindow(startStep)
}

func runWindow(startStep string) {
	title := "WireZTNA"
	width := 440
	height := 580

	if startStep == "status" {
		title = "WireZTNA - Status"
		height = 560
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  uint(width),
			Height: uint(height),
			Center: true,
		},
	})
	if w == nil {
		fmt.Println("[window] WebView2 runtime not available")
		wizard.mu.Lock()
		wizard.open = false
		wizard.mu.Unlock()
		return
	}

	// Make the window resizable with a minimum size so the content is
	// always fully visible regardless of display scaling / DPI.
	w.SetSize(width, height, webview2.HintNone)
	w.SetSize(380, 480, webview2.HintMin)

	defer func() {
		w.Destroy()
		wizard.mu.Lock()
		wizard.open = false
		wizard.webview = nil
		if wizard.stopPoll != nil {
			close(wizard.stopPoll)
			wizard.stopPoll = nil
		}
		wizard.mu.Unlock()
	}()

	wizard.mu.Lock()
	wizard.webview = w
	wizard.mu.Unlock()

	// Bind the action handler — all JS→Go communication goes through this.
	// Slow actions (IPC calls) are dispatched to a goroutine to avoid blocking
	// the WebView2 UI thread, which would trigger "Not Responding" on Windows.
	w.Bind("wizardAction", func(actionJSON string) string {
		return handleActionDispatch(w, actionJSON)
	})

	// Bind a status poller for the status view.
	// This returns immediately and pushes the result via callback to avoid
	// blocking the UI thread during IPC.
	w.Bind("getStatus", func() string {
		go func() {
			statusJSON := getStatusJSON()
			wizard.mu.Lock()
			wv := wizard.webview
			wizard.mu.Unlock()
			if wv != nil {
				wv.Dispatch(func() {
					wv.Eval(fmt.Sprintf(`updateStatus(%s)`, statusJSON))
				})
			}
		}()
		return `{"status":"loading"}`
	})

	w.SetHtml(buildHTML(startStep))

	// If showing status, start auto-refresh poller
	if startStep == "status" {
		go statusPoller(w)
	}

	// Watch for close signal from the "close" action handler.
	// Terminate() must be called from outside Dispatch() to avoid deadlock
	// that causes "not responding" on Windows.
	go func() {
		wizard.mu.Lock()
		ch := wizard.closeCh
		wizard.mu.Unlock()
		if ch == nil {
			return
		}
		<-ch
		w.Terminate()
	}()

	w.Run()
}

// statusPoller pushes state updates to the webview every 2 seconds.
// It serializes polls: if a previous IPC call is still in-flight, the tick is skipped.
func statusPoller(w webview2.WebView) {
	wizard.mu.Lock()
	stopCh := wizard.stopPoll
	wizard.mu.Unlock()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	polling := make(chan struct{}, 1) // capacity 1 = at most one in-flight poll

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			wizard.mu.Lock()
			wv := wizard.webview
			wizard.mu.Unlock()
			if wv == nil {
				return
			}
			// Skip if previous poll is still in-flight (service slow/busy)
			select {
			case polling <- struct{}{}:
				go func() {
					defer func() { <-polling }()
					statusJSON := getStatusJSON()
					wizard.mu.Lock()
					wv2 := wizard.webview
					wizard.mu.Unlock()
					if wv2 != nil {
						wv2.Dispatch(func() {
							wv2.Eval(fmt.Sprintf(`updateStatus(%s)`, statusJSON))
						})
					}
				}()
			default:
				// Previous poll still running — skip this tick
			}
		}
	}
}

func getStatusJSON() string {
	state, err := wizard.ipc.SendStatus()
	if err != nil {
		return `{"status":"service_unavailable","error":"` + err.Error() + `"}`
	}
	b, _ := json.Marshal(state)
	return string(b)
}

// actionInFlight prevents concurrent IPC actions from the wizard UI.
// If a previous action is still running, new ones are dropped.
var actionInFlight = make(chan struct{}, 1)

// handleActionDispatch routes actions from JS. Instant actions (close) return
// immediately. Slow actions (IPC calls) are dispatched to a goroutine so the
// WebView2 message pump keeps running — the result is pushed back to JS via
// Dispatch+Eval calling onActionResult(json).
func handleActionDispatch(w webview2.WebView, actionJSON string) string {
	var action struct {
		Type      string `json:"type"`
		EnrollURL string `json:"enroll_url,omitempty"`
		OTPCode   string `json:"otp_code,omitempty"`
	}

	if err := json.Unmarshal([]byte(actionJSON), &action); err != nil {
		return `{"status":"error","error":"Invalid request"}`
	}

	switch action.Type {
	case "close":
		// Instant — signal close via channel, Terminate() is called by the
		// watcher goroutine in runWindow(). No Dispatch(Destroy) needed.
		go func() {
			wizard.mu.Lock()
			ch := wizard.closeCh
			wizard.closeCh = nil
			wizard.mu.Unlock()
			if ch != nil {
				close(ch)
			}
		}()
		return jsonOK("closed", "")

	case "enroll", "login", "otp_verify", "connect", "disconnect":
		// Drop if a previous action is still in-flight
		select {
		case actionInFlight <- struct{}{}:
			go func() {
				defer func() { <-actionInFlight }()
				handleActionAsync(w, action.Type, actionJSON)
			}()
			return `{"status":"pending"}`
		default:
			return `{"status":"pending"}`
		}

	default:
		return jsonErr("unknown action: " + action.Type)
	}
}

// handleActionAsync executes the IPC call in the background and pushes the
// result to the WebView2 JS context via Dispatch. This keeps the UI responsive.
func handleActionAsync(w webview2.WebView, actionType string, actionJSON string) {
	var action struct {
		Type      string `json:"type"`
		EnrollURL string `json:"enroll_url,omitempty"`
		OTPCode   string `json:"otp_code,omitempty"`
	}
	_ = json.Unmarshal([]byte(actionJSON), &action)

	var result string

	switch actionType {
	case "enroll":
		err := wizard.ipc.SendEnroll(action.EnrollURL)
		if err != nil {
			result = jsonErr(err.Error())
			break
		}
		// After enrollment, request OTP
		state, err := wizard.ipc.SendLogin()
		if err != nil {
			result = jsonOK("enrolled", "")
			break
		}
		email := ""
		if state != nil {
			email = state.Email
		}
		result = jsonOK("login_requested", email)

	case "login":
		state, err := wizard.ipc.SendLogin()
		if err != nil {
			result = jsonErr(err.Error())
			break
		}
		email := ""
		if state != nil {
			email = state.Email
		}
		result = jsonOK("login_requested", email)

	case "otp_verify":
		err := wizard.ipc.SendOTPVerify(action.OTPCode)
		if err != nil {
			result = jsonErr(err.Error())
			break
		}
		// Connect automatically after successful login
		time.Sleep(200 * time.Millisecond)
		_ = wizard.ipc.SendConnect("")
		wizard.mu.Lock()
		onDone := wizard.onDone
		wizard.mu.Unlock()
		if onDone != nil {
			onDone()
		}
		result = jsonOK("connected", "")

	case "connect":
		_ = wizard.ipc.SendConnect("")
		wizard.mu.Lock()
		onDone := wizard.onDone
		wizard.mu.Unlock()
		if onDone != nil {
			onDone()
		}
		result = jsonOK("connecting", "")

	case "disconnect":
		_ = wizard.ipc.SendDisconnect()
		result = jsonOK("disconnected", "")
	}

	// Push result back to JS on the UI thread
	wizard.mu.Lock()
	wv := wizard.webview
	wizard.mu.Unlock()
	if wv != nil {
		escapedResult := result
		wv.Dispatch(func() {
			wv.Eval(fmt.Sprintf(`onActionResult(%s)`, escapedResult))
		})
	}
}

func jsonOK(status, data string) string {
	m := map[string]string{"status": status}
	if data != "" {
		m["data"] = data
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func jsonErr(msg string) string {
	b, _ := json.Marshal(map[string]string{"status": "error", "error": msg})
	return string(b)
}

// buildHTML returns the full HTML for the window.
func buildHTML(startStep string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>
:root{--bg-deep:#0d0f1a;--bg-surface:#151829;--bg-elevated:#1c2039;--bg-card:#1e2242;--border:#2a2f4f;--border-focus:#7c6aff;--text-primary:#f0f0f5;--text-secondary:#8b8fa8;--text-muted:#5a5e78;--accent:#7c6aff;--accent-dim:#5b4fcf;--accent-glow:rgba(124,106,255,0.15);--success:#34d399;--success-dim:rgba(52,211,153,0.12);--danger:#f87171;--danger-dim:rgba(248,113,113,0.12);--amber:#fbbf24;--radius:10px;--radius-lg:14px;--shadow:0 4px 24px rgba(0,0,0,0.4)}
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI Variable','Segoe UI',-apple-system,BlinkMacSystemFont,sans-serif;background:var(--bg-deep);color:var(--text-primary);min-height:100vh;display:flex;flex-direction:column;overflow-y:auto;user-select:none;-webkit-font-smoothing:antialiased}
body::before{content:'';position:fixed;top:-40%%;left:-20%%;width:140%%;height:80%%;background:radial-gradient(ellipse at 50%% 0%%,rgba(124,106,255,0.06) 0%%,transparent 60%%);pointer-events:none}
.container{flex:1;display:flex;flex-direction:column;justify-content:center;padding:2rem 2.2rem;position:relative;z-index:1}
.header{text-align:center;margin-bottom:1.8rem}
.header .icon-wrap{width:52px;height:52px;margin:0 auto 0.9rem;display:flex;align-items:center;justify-content:center;background:var(--accent-glow);border:1px solid rgba(124,106,255,0.2);border-radius:14px;backdrop-filter:blur(8px)}
.header .icon-wrap svg{width:26px;height:26px}
.header h1{font-size:1.15rem;font-weight:600;color:var(--text-primary);letter-spacing:-0.01em;margin-bottom:0.3rem}
.header p{font-size:0.8rem;color:var(--text-secondary);line-height:1.5}
.header p strong{color:var(--text-primary);font-weight:500}
.step{display:none;flex-direction:column;gap:1rem;animation:fadeSlideIn 0.3s ease}
.step.active{display:flex}
@keyframes fadeSlideIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:translateY(0)}}
label{font-size:0.72rem;color:var(--text-secondary);font-weight:500;text-transform:uppercase;letter-spacing:0.4px;margin-bottom:0.2rem;display:block}
.input-group{position:relative}
input[type="text"],input[type="url"]{width:100%%;padding:0.7rem 0.9rem;background:var(--bg-elevated);border:1.5px solid var(--border);border-radius:var(--radius);color:var(--text-primary);font-size:0.84rem;outline:none;transition:border-color 0.2s,box-shadow 0.2s,background 0.2s}
input:focus{border-color:var(--border-focus);box-shadow:0 0 0 3px var(--accent-glow);background:var(--bg-card)}
input::placeholder{color:var(--text-muted)}
.otp-inputs{display:flex;gap:0.5rem;justify-content:center;margin:0.5rem 0}
.otp-inputs input{width:44px;height:54px;text-align:center;font-size:1.4rem;font-weight:700;padding:0;border-radius:var(--radius);background:var(--bg-elevated);border:1.5px solid var(--border);color:var(--text-primary);caret-color:var(--accent);transition:border-color 0.2s,box-shadow 0.2s,transform 0.15s}
.otp-inputs input:focus{border-color:var(--border-focus);box-shadow:0 0 0 3px var(--accent-glow);transform:translateY(-2px)}
.otp-inputs input.filled{border-color:var(--accent);background:rgba(124,106,255,0.06)}
.btn{width:100%%;padding:0.72rem;background:var(--accent);color:#fff;border:none;border-radius:var(--radius);font-size:0.84rem;font-weight:600;cursor:pointer;transition:all 0.2s;position:relative;overflow:hidden}
.btn:hover{background:var(--accent-dim);transform:translateY(-1px);box-shadow:0 4px 16px rgba(124,106,255,0.3)}
.btn:active{transform:translateY(0);box-shadow:none}
.btn:disabled{opacity:0.35;cursor:not-allowed;transform:none;box-shadow:none}
.btn.secondary{background:transparent;border:1.5px solid var(--border);color:var(--text-secondary);font-size:0.78rem;padding:0.55rem}
.btn.secondary:hover{border-color:var(--border-focus);color:var(--text-primary);background:var(--accent-glow);transform:translateY(-1px)}
.btn.danger{background:var(--danger);color:#fff}
.btn.danger:hover{background:#ef4444;box-shadow:0 4px 16px rgba(248,113,113,0.3)}
.error{background:var(--danger-dim);border:1px solid rgba(248,113,113,0.25);color:var(--danger);padding:0.6rem 0.8rem;border-radius:var(--radius);font-size:0.76rem;display:none;line-height:1.4;backdrop-filter:blur(4px)}
.error.show{display:block;animation:fadeSlideIn 0.2s ease}
.success-box{text-align:center;padding:2rem 0}
.success-box .icon-wrap{width:64px;height:64px;margin:0 auto 1rem;display:flex;align-items:center;justify-content:center;background:var(--success-dim);border:1px solid rgba(52,211,153,0.25);border-radius:50%%;animation:pulseSuccess 2s ease infinite}
.success-box .icon-wrap svg{width:30px;height:30px;color:var(--success)}
.success-box h2{color:var(--success);font-size:1.1rem;font-weight:600;margin-bottom:0.35rem}
.success-box p{color:var(--text-secondary);font-size:0.8rem}
@keyframes pulseSuccess{0%%,100%%{box-shadow:0 0 0 0 rgba(52,211,153,0.2)}50%%{box-shadow:0 0 0 12px rgba(52,211,153,0)}}
.spinner{width:32px;height:32px;border:3px solid var(--border);border-top-color:var(--accent);border-radius:50%%;animation:spin 0.8s linear infinite;margin:1rem auto}
@keyframes spin{to{transform:rotate(360deg)}}
.status-header{display:flex;align-items:center;gap:0.8rem;margin-bottom:1.2rem}
.status-indicator{width:10px;height:10px;border-radius:50%%;background:var(--text-muted);flex-shrink:0}
.status-indicator.online{background:var(--success);box-shadow:0 0 8px rgba(52,211,153,0.5)}
.status-indicator.connecting{background:var(--amber);animation:pulse 1.5s infinite}
.status-indicator.offline{background:var(--text-muted)}
@keyframes pulse{0%%,100%%{opacity:1}50%%{opacity:0.4}}
.status-grid{display:grid;grid-template-columns:1fr 1fr;gap:0.55rem}
.stat{background:var(--bg-elevated);border:1px solid var(--border);border-radius:var(--radius);padding:0.7rem 0.85rem;transition:border-color 0.2s}
.stat:hover{border-color:rgba(124,106,255,0.2)}
.stat label{font-size:0.62rem;text-transform:uppercase;letter-spacing:0.6px;color:var(--text-muted);display:block;margin-bottom:0.25rem;font-weight:500}
.stat .value{font-size:0.9rem;color:var(--text-primary);font-weight:600;font-variant-numeric:tabular-nums}
.stat .value.green{color:var(--success)}
.stat .value.amber{color:var(--amber)}
.stat .value.red{color:var(--danger)}
.stat.full{grid-column:1/-1}
.traffic-row{display:flex;gap:0.55rem}
.traffic-row .stat{flex:1;text-align:center}
.traffic-row .stat .value{font-size:0.95rem}
.traffic-row .stat .arrow{font-size:0.7rem;color:var(--text-muted);margin-right:0.2rem}
.footer{text-align:center;padding:0.7rem;font-size:0.62rem;color:var(--text-muted);letter-spacing:0.3px}
.actions{display:flex;gap:0.6rem;margin-top:0.6rem}
.actions .btn{flex:1}
.brand-bar{display:flex;align-items:center;justify-content:center;gap:0.5rem;margin-bottom:0.3rem}
.brand-bar svg{width:18px;height:18px;color:var(--accent)}
.brand-bar span{font-size:0.9rem;font-weight:600;color:var(--text-primary);letter-spacing:-0.02em}
</style></head><body>
<div class="container">

<!-- Step: Enroll -->
<div class="step" id="step-enroll">
<div class="header">
<div class="icon-wrap"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color:var(--accent)"><path d="M12 2L3 7v6c0 5.5 3.8 10.7 9 12 5.2-1.3 9-6.5 9-12V7l-9-5z"/><polyline points="9 12 11 14 15 10"/></svg></div>
<h1>Welcome to WireZTNA</h1>
<p>Paste your enrollment URL to register this device securely.</p>
</div>
<div class="input-group"><label>Enrollment URL</label><input type="url" id="enroll-url" placeholder="https://...enroll?token=..." autofocus></div>
<div class="error" id="enroll-error"></div>
<button class="btn" id="btn-enroll" onclick="doEnroll()">Enroll this device</button>
</div>

<!-- Step: Login -->
<div class="step" id="step-login">
<div class="header">
<div class="icon-wrap"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color:var(--accent)"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M22 7l-10 6L2 7"/></svg></div>
<h1>Sign In</h1>
<p>A 6-digit access code will be sent to your registered email.</p>
</div>
<div class="error" id="login-error"></div>
<button class="btn" id="btn-login" onclick="doLogin()">Send access code</button>
</div>

<!-- Step: OTP -->
<div class="step" id="step-otp">
<div class="header">
<div class="icon-wrap"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color:var(--accent)"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/><circle cx="12" cy="16" r="1"/></svg></div>
<h1>Enter Code</h1>
<p>Sent to <strong id="otp-email"></strong></p>
</div>
<div class="otp-inputs">
<input type="text" maxlength="1" class="otp-digit" data-idx="0" inputmode="numeric" autofocus>
<input type="text" maxlength="1" class="otp-digit" data-idx="1" inputmode="numeric">
<input type="text" maxlength="1" class="otp-digit" data-idx="2" inputmode="numeric">
<input type="text" maxlength="1" class="otp-digit" data-idx="3" inputmode="numeric">
<input type="text" maxlength="1" class="otp-digit" data-idx="4" inputmode="numeric">
<input type="text" maxlength="1" class="otp-digit" data-idx="5" inputmode="numeric">
</div>
<div class="error" id="otp-error"></div>
<button class="btn" id="btn-verify" onclick="doVerify()" disabled>Verify code</button>
<button class="btn secondary" onclick="doLogin()">Resend code</button>
</div>

<!-- Step: Done -->
<div class="step" id="step-done">
<div class="success-box">
<div class="icon-wrap"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg></div>
<h2>You're connected</h2>
<p>WireZTNA is active and securing your traffic.</p>
</div>
</div>

<!-- Step: Status (live connection info) -->
<div class="step" id="step-status">
<div class="status-header">
<div class="status-indicator" id="st-dot"></div>
<div>
<div class="brand-bar"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2L3 7v6c0 5.5 3.8 10.7 9 12 5.2-1.3 9-6.5 9-12V7l-9-5z"/></svg><span>WireZTNA</span></div>
<p id="status-subtitle" style="font-size:0.75rem;color:var(--text-secondary)">Checking...</p>
</div>
</div>
<div class="status-grid">
<div class="stat"><label>Status</label><div class="value" id="st-status">&mdash;</div></div>
<div class="stat"><label>Overlay IP</label><div class="value" id="st-ip">&mdash;</div></div>
</div>
<div class="traffic-row">
<div class="stat"><label>Download</label><div class="value" id="st-rx"><span class="arrow">&darr;</span>&mdash;</div></div>
<div class="stat"><label>Upload</label><div class="value" id="st-tx"><span class="arrow">&uarr;</span>&mdash;</div></div>
</div>
<div class="status-grid" style="margin-top:0.55rem">
<div class="stat"><label>Handshake</label><div class="value" id="st-handshake">&mdash;</div></div>
<div class="stat"><label>Session</label><div class="value" id="st-session">&mdash;</div></div>
<div class="stat full"><label>Mode</label><div class="value" id="st-mode">&mdash;</div></div>
</div>
<div class="actions" id="status-actions">
<button class="btn danger" onclick="doAction('disconnect')">Disconnect</button>
</div>
<div class="actions" id="status-actions-disconnected" style="display:none">
<button class="btn" onclick="doAction('connect')">Connect</button>
</div>
</div>

</div>
<div class="footer">WireZTNA v%s</div>

<script>
function goToStep(s){
  document.querySelectorAll('.step').forEach(e=>{e.classList.remove('active');e.style.animation='none'});
  var el=document.getElementById('step-'+s);
  if(el){el.offsetHeight;el.style.animation='';el.classList.add('active')}
  setTimeout(()=>{var inp=document.querySelector('.step.active input');if(inp)inp.focus()},80);
}
goToStep('%s');

// Async result handler — Go calls this via Dispatch+Eval when IPC completes.
// Each action sets a _pendingAction so we know how to route the result.
var _pendingAction='';
function onActionResult(r){
  if(!r)return;
  switch(_pendingAction){
  case 'enroll':
    if(r.status==='error'){showErr('enroll-error',r.error);resetBtn('btn-enroll','Enroll this device');return}
    if(r.data)document.getElementById('otp-email').textContent=r.data;goToStep('otp');break;
  case 'login':
    if(r.status==='error'){showErr('login-error',r.error);resetBtn('btn-login','Send access code');return}
    if(r.data)document.getElementById('otp-email').textContent=r.data;goToStep('otp');break;
  case 'otp_verify':
    if(r.status==='error'){showErr('otp-error',r.error);resetBtn('btn-verify','Verify code');
      document.querySelectorAll('.otp-digit').forEach(d=>{d.value='';d.classList.remove('filled')});
      document.querySelectorAll('.otp-digit')[0].focus();return}
    goToStep('done');break;
  case 'connect':case 'disconnect':
    // Trigger immediate status refresh so UI updates without waiting for next poll
    getStatus();break;
  }
  _pendingAction='';
}
function showErr(id,msg){var e=document.getElementById(id);if(e){e.textContent=msg;e.classList.add('show')}}
function resetBtn(id,txt){var b=document.getElementById(id);if(b){b.disabled=false;b.textContent=txt}}

// Enroll
async function doEnroll(){
  var url=document.getElementById('enroll-url').value.trim();if(!url)return;
  var btn=document.getElementById('btn-enroll'),err=document.getElementById('enroll-error');
  btn.disabled=true;btn.textContent='Enrolling...';err.classList.remove('show');
  _pendingAction='enroll';
  try{var r=JSON.parse(await wizardAction(JSON.stringify({type:'enroll',enroll_url:url})));
  if(r.status==='pending')return;
  if(r.status==='error'){err.textContent=r.error;err.classList.add('show');btn.disabled=false;btn.textContent='Enroll this device';return}
  if(r.data)document.getElementById('otp-email').textContent=r.data;goToStep('otp')}
  catch(e){err.textContent='Connection error';err.classList.add('show');btn.disabled=false;btn.textContent='Enroll this device'}}

// Login
async function doLogin(){
  var err=document.getElementById('login-error'),btn=document.getElementById('btn-login');
  if(btn){btn.disabled=true;btn.textContent='Sending...'}if(err)err.classList.remove('show');
  _pendingAction='login';
  try{var r=JSON.parse(await wizardAction(JSON.stringify({type:'login'})));
  if(r.status==='pending')return;
  if(r.status==='error'){if(err){err.textContent=r.error;err.classList.add('show')}if(btn){btn.disabled=false;btn.textContent='Send access code'}return}
  if(r.data)document.getElementById('otp-email').textContent=r.data;goToStep('otp')}
  catch(e){if(err){err.textContent='Connection error';err.classList.add('show')}if(btn){btn.disabled=false;btn.textContent='Send access code'}}}

// OTP
async function doVerify(){
  var digits=document.querySelectorAll('.otp-digit'),code='';digits.forEach(d=>code+=d.value);if(code.length!==6)return;
  var btn=document.getElementById('btn-verify'),err=document.getElementById('otp-error');
  btn.disabled=true;btn.textContent='Verifying...';err.classList.remove('show');
  _pendingAction='otp_verify';
  try{var r=JSON.parse(await wizardAction(JSON.stringify({type:'otp_verify',otp_code:code})));
  if(r.status==='pending')return;
  if(r.status==='error'){err.textContent=r.error;err.classList.add('show');btn.disabled=false;btn.textContent='Verify code';digits.forEach(d=>{d.value='';d.classList.remove('filled')});digits[0].focus();return}
  goToStep('done')}catch(e){err.textContent='Error';err.classList.add('show');btn.disabled=false;btn.textContent='Verify code'}}

async function doAction(type){
  _pendingAction=type;
  // Immediate visual feedback
  var actBtn=document.querySelector('#status-actions .btn, #status-actions-disconnected .btn');
  if(actBtn){actBtn.disabled=true;actBtn.textContent=type==='connect'?'Connecting...':'Disconnecting...';}
  try{await wizardAction(JSON.stringify({type:type}))}catch(e){}
}
function doClose(){wizardAction(JSON.stringify({type:'close'})).catch(function(){})}

// OTP input handling
document.querySelectorAll('.otp-digit').forEach((inp,idx,all)=>{
  inp.addEventListener('input',e=>{
    var v=e.target.value.replace(/\D/g,'');e.target.value=v?v[0]:'';
    e.target.classList.toggle('filled',!!v);
    if(v&&idx<5)all[idx+1].focus();checkOTP()});
  inp.addEventListener('keydown',e=>{if(e.key==='Backspace'&&!e.target.value&&idx>0){all[idx-1].focus();all[idx-1].classList.remove('filled')}if(e.key==='Enter')doVerify()});
  inp.addEventListener('paste',e=>{e.preventDefault();var p=(e.clipboardData.getData('text')||'').replace(/\D/g,'').slice(0,6);for(var i=0;i<p.length&&i<6;i++){all[i].value=p[i];all[i].classList.add('filled')}if(p.length>=6)all[5].focus();checkOTP()})});
function checkOTP(){var c='';document.querySelectorAll('.otp-digit').forEach(d=>c+=d.value);document.getElementById('btn-verify').disabled=c.length!==6}

document.getElementById('enroll-url').addEventListener('keydown',e=>{if(e.key==='Enter')doEnroll()});

// Status auto-update (called from Go every 2s)
function updateStatus(st){
  if(!st)return;
  var el=document.getElementById('st-status');if(!el)return;
  el.textContent=st.status||'unknown';
  el.className='value '+(st.status==='connected'?'green':st.status==='error'?'red':'amber');
  // Update status dot
  var dot=document.getElementById('st-dot');
  if(dot){dot.className='status-indicator '+(st.status==='connected'?'online':st.status==='connecting'||st.status==='reconnecting'?'connecting':'offline')}
  document.getElementById('st-ip').textContent=st.overlay_ip||'\u2014';
  document.getElementById('st-rx').innerHTML='<span class="arrow">\u2193</span>'+fmtBytes(st.rx_bytes||0);
  document.getElementById('st-tx').innerHTML='<span class="arrow">\u2191</span>'+fmtBytes(st.tx_bytes||0);
  var hs=st.last_handshake?timeSince(st.last_handshake):'\u2014';
  document.getElementById('st-handshake').textContent=hs;
  var exp=st.expires_at?timeUntil(st.expires_at):'\u2014';
  document.getElementById('st-session').textContent=exp;
  var mode='Split Tunnel';
  if(st.is_exit_node)mode='VPN: '+(st.exit_node_name||'Exit Node');
  else if(st.group_name)mode=st.group_name;
  document.getElementById('st-mode').textContent=mode;
  document.getElementById('status-subtitle').textContent=st.status==='connected'?'Connected and secured':'Disconnected';
  document.getElementById('status-actions').style.display=st.status==='connected'?'flex':'none';
  document.getElementById('status-actions-disconnected').style.display=st.status!=='connected'?'flex':'none';
}

function fmtBytes(b){if(b>=1073741824)return(b/1073741824).toFixed(1)+' GB';if(b>=1048576)return(b/1048576).toFixed(1)+' MB';if(b>=1024)return(b/1024).toFixed(0)+' KB';return b+' B'}
function timeSince(iso){var d=new Date(iso);var s=Math.floor((Date.now()-d.getTime())/1000);if(s<60)return s+'s ago';if(s<3600)return Math.floor(s/60)+'m ago';return Math.floor(s/3600)+'h ago'}
function timeUntil(iso){var d=new Date(iso);var s=Math.floor((d.getTime()-Date.now())/1000);if(s<=0)return'expired';if(s<3600)return Math.floor(s/60)+'m left';return Math.floor(s/3600)+'h '+Math.floor((s%%3600)/60)+'m left'}

// Initial status fetch for status view
if('%s'==='status'){getStatus().then(r=>{try{updateStatus(JSON.parse(r))}catch(e){}})}
</script></body></html>`, version.Version, startStep, startStep)
}
